package raft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/adcodelabs/kvaldb/internal/kv"
	kvaldbpb "github.com/adcodelabs/kvaldb/internal/rpc"
	"github.com/adcodelabs/kvaldb/internal/storage"
)

var ErrNotLeader = errors.New("not leader")
var ErrNoLeader = errors.New("no leader")

// PeerTransport sends Raft RPCs to a peer gRPC address.
type PeerTransport interface {
	RequestVote(ctx context.Context, peerAddr string, req *kvaldbpb.RequestVoteRequest) (*kvaldbpb.RequestVoteResponse, error)
	AppendEntries(ctx context.Context, peerAddr string, req *kvaldbpb.AppendEntriesRequest) (*kvaldbpb.AppendEntriesResponse, error)
}

// Node is a single Raft peer
type Node struct {
	mu sync.Mutex

	st       *storage.Store
	selfID   string
	selfAddr string
	peers    map[string]string // id -> gRPC dial address (includes self)

	// quiescent: joining nodes do not start elections until they receive AppendEntries
	quiescent bool

	currentTerm uint64
	votedFor    string
	log         []storage.LogEntry

	commitIndex uint64
	lastApplied uint64

	role     Role
	leaderID string

	nextIndex  map[string]uint64
	matchIndex map[string]uint64

	transport PeerTransport
	rng       *rand.Rand

	electionDeadline time.Time

	stopCh chan struct{}
	wg     sync.WaitGroup
}

type Config struct {
	SelfID       string
	SelfAddr     string
	DataDir      string
	Bootstrap    bool
	Transport    PeerTransport
	InitialPeers map[string]string // only for bootstrap
}

// NewNode creates and opens storage
func NewNode(cfg Config) (*Node, error) {
	if cfg.Transport == nil {
		return nil, errors.New("raft: Transport is required")
	}
	st, err := storage.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	meta, err := st.GetMeta()
	if err != nil {
		return nil, err
	}
	logEntries, err := st.Log()
	if err != nil {
		return nil, err
	}

	n := &Node{
		st:          st,
		selfID:      cfg.SelfID,
		selfAddr:    cfg.SelfAddr,
		peers:       make(map[string]string),
		currentTerm: meta.CurrentTerm,
		votedFor:    meta.VotedFor,
		log:         logEntries,
		transport:   cfg.Transport,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
		nextIndex:   make(map[string]uint64),
		matchIndex:  make(map[string]uint64),
		stopCh:      make(chan struct{}),
		role:        RoleFollower,
	}
	n.stepf("init storage_loaded term=%d voted_for=%q log_entries=%d bootstrap=%v",
		meta.CurrentTerm, meta.VotedFor, len(logEntries), cfg.Bootstrap)

	if cfg.Bootstrap {
		n.quiescent = false
		if cfg.InitialPeers == nil {
			cfg.InitialPeers = map[string]string{cfg.SelfID: cfg.SelfAddr}
		}
		if _, ok := cfg.InitialPeers[cfg.SelfID]; !ok {
			cfg.InitialPeers[cfg.SelfID] = cfg.SelfAddr
		}
		if len(n.log) == 1 && n.lastLogIndex() == 0 {
			data, err := kv.MarshalConfig(cfg.InitialPeers)
			if err != nil {
				return nil, err
			}
			n.currentTerm = 1
			n.votedFor = ""
			n.log = append(n.log, storage.LogEntry{Index: 1, Term: 1, Type: kv.EntryConfig, Data: data})
			n.peers = clonePeers(cfg.InitialPeers)
			n.commitIndex = 1
			if err := n.persistLocked(); err != nil {
				return nil, err
			}
			n.applyToLastApplied()
			n.becomeLeaderLocked()
			n.stepf("bootstrap fresh_cluster CONFIG@1 term=1 became_leader peers=%d", len(n.peers))
		} else if p, err := n.peersFromLogLocked(); err == nil && len(p) > 0 {
			n.peers = clonePeers(p)
			n.stepf("bootstrap reload_from_disk peers=%d (will run elections as follower)", len(n.peers))
		}
	} else {
		n.quiescent = true
		n.peers = map[string]string{cfg.SelfID: cfg.SelfAddr}
		n.stepf("joiner mode quiescent=true (elections disabled until first AppendEntries from a leader)")
	}

	if len(n.peers) == 0 {
		if p, err := n.peersFromLogLocked(); err == nil && len(p) > 0 {
			n.peers = clonePeers(p)
			n.stepf("peers recovered from log count=%d", len(n.peers))
		}
	}

	n.resetElectionTimerLocked()
	n.stepf("init done role=%s term=%d leader=%q peer_count=%d quiescent=%v next_election_deadline≈%s",
		n.role.String(), n.currentTerm, n.leaderID, len(n.peers), n.quiescent, n.electionDeadline.Format(time.RFC3339Nano))
	return n, nil
}

// WakeAfterJoin disables quiescent mode after a successful Join RPC
func (n *Node) WakeAfterJoin() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.quiescent = false
	n.stepf("WakeAfterJoin: quiescent=false")
}

func clonePeers(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// runs election and heartbeat loops.
func (n *Node) Start() {
	n.stepf("Start: launching election_loop heartbeat_loop applier")
	n.wg.Add(3)
	go n.runElectionLoop()
	go n.runHeartbeatLoop()
	go n.runApplier()
}

func (n *Node) Stop() {
	n.stepf("Stop: shutting down background loops")
	close(n.stopCh)
	n.wg.Wait()
}

func (n *Node) runApplier() {
	defer n.wg.Done()
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-n.stopCh:
			n.stepf("applier: stopped")
			return
		case <-t.C:
			n.mu.Lock()
			n.applyToLastApplied()
			n.mu.Unlock()
		}
	}
}

func (n *Node) runElectionLoop() {
	defer n.wg.Done()
	t := time.NewTicker(25 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-n.stopCh:
			n.stepf("election_loop: stopped")
			return
		case <-t.C:
			n.tickElection()
		}
	}
}

func (n *Node) runHeartbeatLoop() {
	defer n.wg.Done()
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-n.stopCh:
			n.stepf("heartbeat_loop: stopped")
			return
		case <-t.C:
			n.replicateHeartbeat()
		}
	}
}

func (n *Node) tickElection() {
	n.mu.Lock()
	if n.role == RoleLeader {
		n.mu.Unlock()
		return
	}
	if n.quiescent {
		n.resetElectionTimerLocked()
		n.stepf("tick_election: quiescent skip_election (reset timer) next_deadline=%s", n.electionDeadline.Format(time.RFC3339Nano))
		n.mu.Unlock()
		return
	}
	if time.Now().Before(n.electionDeadline) {
		n.mu.Unlock()
		return
	}
	n.stepf("tick_election: deadline_expired role=%s term=%d leader=%q -> start_election",
		n.role.String(), n.currentTerm, n.leaderID)
	n.mu.Unlock()
	n.runCandidateElection()
}

func (n *Node) resetElectionTimerLocked() {
	d := electionTimeoutMin + time.Duration(n.rng.Int63n(int64(electionTimeoutMax-electionTimeoutMin)))
	n.electionDeadline = time.Now().Add(d)
}

func (n *Node) lastLogIndex() uint64 {
	if len(n.log) < 2 {
		return 0
	}
	return n.log[len(n.log)-1].Index
}

func (n *Node) lastLogTerm() uint64 {
	if len(n.log) < 2 {
		return 0
	}
	return n.log[len(n.log)-1].Term
}

func (n *Node) persistLocked() error {
	if err := n.st.SetMeta(storage.Meta{CurrentTerm: n.currentTerm, VotedFor: n.votedFor}); err != nil {
		return err
	}
	if err := n.st.SetLog(n.log); err != nil {
		return err
	}
	return nil
}

func (n *Node) becomeFollowerLocked(term uint64, leaderID string) {
	was := n.role
	prevLeader := n.leaderID
	n.role = RoleFollower
	n.leaderID = leaderID
	if term > n.currentTerm {
		n.currentTerm = term
		n.votedFor = ""
	}
	n.resetElectionTimerLocked()
	n.stepf("transition -> follower was_role=%s prev_leader=%q now_term=%d now_leader=%q incoming_term_arg=%d",
		was.String(), prevLeader, n.currentTerm, n.leaderID, term)
}

func (n *Node) becomeLeaderLocked() {
	was := n.role
	n.role = RoleLeader
	n.leaderID = n.selfID
	for id := range n.peers {
		n.nextIndex[id] = n.lastLogIndex() + 1
		if n.nextIndex[id] < 1 {
			n.nextIndex[id] = 1
		}
		n.matchIndex[id] = 0
	}
	n.matchIndex[n.selfID] = n.lastLogIndex()
	n.stepf("transition -> leader was_role=%s term=%d last_log_index=%d peer_count=%d match_self=%d",
		was.String(), n.currentTerm, n.lastLogIndex(), len(n.peers), n.matchIndex[n.selfID])
}

func (n *Node) runCandidateElection() {
	n.mu.Lock()
	wasRole := n.role
	n.role = RoleCandidate
	n.currentTerm++
	n.votedFor = n.selfID
	n.leaderID = ""
	term := n.currentTerm
	lastIdx := n.lastLogIndex()
	lastTerm := n.lastLogTerm()
	peers := clonePeers(n.peers)
	selfID := n.selfID
	_ = n.persistLocked()
	n.resetElectionTimerLocked()
	n.stepf("election: start_candidate was_role=%s new_term=%d last_log=(%d,%d) peer_count=%d (need %d votes)",
		wasRole.String(), term, lastIdx, lastTerm, len(peers), (len(peers)/2)+1)
	n.mu.Unlock()

	votes := 1
	var voteMu sync.Mutex
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for peerID, addr := range peers {
		if peerID == selfID {
			continue
		}
		wg.Add(1)
		go func(peerID, addr string) {
			defer wg.Done()
			n.stepf("election: RequestVote -> peer=%s addr=%s term=%d", peerID, addr, term)
			resp, err := n.transport.RequestVote(ctx, addr, &kvaldbpb.RequestVoteRequest{
				Term:         term,
				CandidateId:  selfID,
				LastLogIndex: lastIdx,
				LastLogTerm:  lastTerm,
			})
			if err != nil || resp == nil {
				n.stepf("election: RequestVote <- peer=%s err=%v resp_nil=%v", peerID, err, resp == nil)
				return
			}
			n.stepf("election: RequestVote <- peer=%s reply_term=%d grant=%v", peerID, resp.Term, resp.VoteGranted)
			n.mu.Lock()
			if resp.Term > n.currentTerm {
				n.becomeFollowerLocked(resp.Term, "")
			}
			curTerm := n.currentTerm
			role := n.role
			n.mu.Unlock()
			if resp.Term > curTerm || resp.Term != term {
				return
			}
			if role != RoleCandidate {
				return
			}
			if !resp.VoteGranted {
				return
			}
			voteMu.Lock()
			votes++
			v := votes
			voteMu.Unlock()
			n.stepf("election: vote_granted from=%s total_votes=%d", peerID, v)
		}(peerID, addr)
	}
	wg.Wait()

	n.mu.Lock()
	if n.currentTerm != term || n.role != RoleCandidate {
		n.stepf("election: aborted (stale) now_term=%d now_role=%s expected_term=%d", n.currentTerm, n.role.String(), term)
		n.mu.Unlock()
		return
	}
	won := votes >= (len(peers)/2)+1
	if won {
		n.becomeLeaderLocked()
		_ = n.persistLocked()
		n.stepf("election: won votes=%d/%d -> leader term=%d", votes, len(peers), term)
	} else {
		n.stepf("election: lost votes=%d need>=%d for majority of %d peers", votes, (len(peers)/2)+1, len(peers))
	}
	n.mu.Unlock()
	if won {
		n.stepf("election: replicate_all_peers after_win")
		n.replicateAllPeers(context.Background())
	}
}

func (n *Node) RequestVote(req *kvaldbpb.RequestVoteRequest) (*kvaldbpb.RequestVoteResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.stepf("rpc RequestVote <- candidate=%s term=%d their_last_log=(%d,%d) our_term=%d our_role=%s voted_for=%q",
		req.CandidateId, req.Term, req.LastLogIndex, req.LastLogTerm, n.currentTerm, n.role.String(), n.votedFor)

	if req.Term < n.currentTerm {
		n.stepf("rpc RequestVote -> reject stale_candidate_term grant=false")
		return &kvaldbpb.RequestVoteResponse{Term: n.currentTerm, VoteGranted: false}, nil
	}
	if req.Term > n.currentTerm {
		n.currentTerm = req.Term
		n.votedFor = ""
		n.role = RoleFollower
		n.leaderID = ""
		n.stepf("rpc RequestVote: step_down newer_term=%d clear_leader", req.Term)
	}

	lastIdx := n.lastLogIndex()
	lastTerm := n.lastLogTerm()
	upToDate := req.LastLogTerm > lastTerm ||
		(req.LastLogTerm == lastTerm && req.LastLogIndex >= lastIdx)

	grant := (n.votedFor == "" || n.votedFor == req.CandidateId) && upToDate
	if grant {
		n.votedFor = req.CandidateId
		n.resetElectionTimerLocked()
		_ = n.persistLocked()
		n.stepf("rpc RequestVote -> grant candidate=%s (log_up_to_date=%v our_last=(%d,%d))", req.CandidateId, upToDate, lastIdx, lastTerm)
	} else {
		n.stepf("rpc RequestVote -> deny candidate=%s up_to_date=%v voted_for=%q", req.CandidateId, upToDate, n.votedFor)
	}
	return &kvaldbpb.RequestVoteResponse{Term: n.currentTerm, VoteGranted: grant}, nil
}

func (n *Node) AppendEntries(req *kvaldbpb.AppendEntriesRequest) (*kvaldbpb.AppendEntriesResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.stepf("rpc AppendEntries <- leader=%s term=%d prev=(%d,%d) entries=%d leader_commit=%d our_term=%d our_role=%s log_len=%d",
		req.LeaderId, req.Term, req.PrevLogIndex, req.PrevLogTerm, len(req.Entries), req.LeaderCommit,
		n.currentTerm, n.role.String(), len(n.log))

	n.quiescent = false

	if req.Term < n.currentTerm {
		n.stepf("rpc AppendEntries -> reject stale_leader_term")
		return &kvaldbpb.AppendEntriesResponse{Term: n.currentTerm, Success: false}, nil
	}
	if req.Term > n.currentTerm {
		n.currentTerm = req.Term
		n.votedFor = ""
	}
	n.role = RoleFollower
	n.leaderID = req.LeaderId
	n.resetElectionTimerLocked()

	if req.PrevLogIndex > 0 {
		if int(req.PrevLogIndex) >= len(n.log) {
			n.stepf("rpc AppendEntries -> reject log_too_short conflict_index=%d", len(n.log))
			return &kvaldbpb.AppendEntriesResponse{
				Term:          n.currentTerm,
				Success:       false,
				ConflictIndex: uint64(len(n.log)),
			}, nil
		}
		if n.log[req.PrevLogIndex].Term != req.PrevLogTerm {
			confTerm := n.log[req.PrevLogIndex].Term
			ci := req.PrevLogIndex
			for ci > 0 && n.log[ci].Term == confTerm {
				ci--
			}
			conflict := ci + 1
			n.stepf("rpc AppendEntries -> reject term_mismatch at prev index=%d have_term=%d want_term=%d conflict_index=%d",
				req.PrevLogIndex, confTerm, req.PrevLogTerm, conflict)
			return &kvaldbpb.AppendEntriesResponse{Term: n.currentTerm, Success: false, ConflictIndex: conflict}, nil
		}
	}

	base := int(req.PrevLogIndex) + 1
	if base < len(n.log) {
		n.log = n.log[:base]
		n.stepf("rpc AppendEntries: truncate_log to index=%d (exclusive tail removed)", uint64(base))
	}
	for _, ent := range req.Entries {
		e := storage.LogEntry{Index: ent.Index, Term: ent.Term, Type: ent.Type, Data: ent.Data}
		switch {
		case int(e.Index) == len(n.log):
			n.log = append(n.log, e)
			n.stepf("rpc AppendEntries: append index=%d term=%d type=%s", e.Index, e.Term, entryTypeName(e.Type))
		case int(e.Index) < len(n.log):
			n.log[e.Index] = e
			n.stepf("rpc AppendEntries: overwrite index=%d term=%d type=%s", e.Index, e.Term, entryTypeName(e.Type))
		default:
			n.stepf("rpc AppendEntries -> reject gap at index=%d log_len=%d", e.Index, len(n.log))
			return &kvaldbpb.AppendEntriesResponse{Term: n.currentTerm, Success: false, ConflictIndex: uint64(len(n.log))}, nil
		}
	}
	prevCommit := n.commitIndex
	if req.LeaderCommit > n.commitIndex {
		last := n.lastLogIndex()
		if req.LeaderCommit < last {
			n.commitIndex = req.LeaderCommit
		} else {
			n.commitIndex = last
		}
		if n.commitIndex != prevCommit {
			n.stepf("rpc AppendEntries: advance_commit %d -> %d (leader_commit=%d)", prevCommit, n.commitIndex, req.LeaderCommit)
		}
	}
	_ = n.persistLocked()
	n.stepf("rpc AppendEntries -> success persist leader=%s commit_idx=%d", req.LeaderId, n.commitIndex)
	return &kvaldbpb.AppendEntriesResponse{Term: n.currentTerm, Success: true}, nil
}

func (n *Node) replicateHeartbeat() {
	n.mu.Lock()
	if n.role != RoleLeader {
		n.mu.Unlock()
		return
	}
	peers := make(map[string]string, len(n.peers))
	for id, a := range n.peers {
		if id != n.selfID {
			peers[id] = a
		}
	}
	n.stepf("leader_tick: heartbeat/replicate to %d follower(s)", len(peers))
	n.mu.Unlock()
	ctx := context.Background()
	for id, addr := range peers {
		go n.replicateToPeer(ctx, id, addr)
	}
}

func (n *Node) replicateAllPeers(ctx context.Context) {
	n.mu.Lock()
	peers := make(map[string]string, len(n.peers))
	for id, a := range n.peers {
		if id != n.selfID {
			peers[id] = a
		}
	}
	n.stepf("replicate_all: fan-out AppendEntries to %d peer(s)", len(peers))
	n.mu.Unlock()
	for id, addr := range peers {
		go n.replicateToPeer(ctx, id, addr)
	}
}

func (n *Node) replicateToPeer(ctx context.Context, peerID, peerAddr string) {
	n.mu.Lock()
	next := n.nextIndex[peerID]
	if next < 1 {
		next = 1
	}
	prevIndex := next - 1
	var prevTerm uint64
	if prevIndex > 0 && int(prevIndex) < len(n.log) {
		prevTerm = n.log[prevIndex].Term
	}
	last := n.lastLogIndex()
	var entries []*kvaldbpb.LogEntry
	for i := next; i <= last; i++ {
		if int(i) >= len(n.log) {
			break
		}
		e := n.log[i]
		entries = append(entries, &kvaldbpb.LogEntry{
			Index: e.Index,
			Term:  e.Term,
			Type:  e.Type,
			Data:  append([]byte(nil), e.Data...),
		})
	}
	term := n.currentTerm
	leaderID := n.selfID
	commit := n.commitIndex
	n.stepf("replicate: -> peer=%s addr=%s prev=(%d,%d) entries=%d leader_commit=%d next_index=%d",
		peerID, peerAddr, prevIndex, prevTerm, len(entries), commit, next)
	n.mu.Unlock()

	resp, err := n.transport.AppendEntries(ctx, peerAddr, &kvaldbpb.AppendEntriesRequest{
		Term:         term,
		LeaderId:     leaderID,
		PrevLogIndex: prevIndex,
		PrevLogTerm:  prevTerm,
		Entries:      entries,
		LeaderCommit: commit,
	})
	if err != nil || resp == nil {
		n.stepf("replicate: <- peer=%s transport_err=%v resp_nil=%v", peerID, err, resp == nil)
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if resp.Term > n.currentTerm {
		n.stepf("replicate: <- peer=%s discovered_higher_term=%d step_down", peerID, resp.Term)
		n.becomeFollowerLocked(resp.Term, "")
		return
	}
	if n.role != RoleLeader {
		n.stepf("replicate: <- peer=%s ignore (no longer leader role=%s)", peerID, n.role.String())
		return
	}
	if !resp.Success {
		if resp.ConflictIndex > 0 {
			n.nextIndex[peerID] = resp.ConflictIndex
			n.stepf("replicate: <- peer=%s append_failed conflict_index=%d next_index=%d", peerID, resp.ConflictIndex, n.nextIndex[peerID])
		} else if n.nextIndex[peerID] > 1 {
			n.nextIndex[peerID]--
			n.stepf("replicate: <- peer=%s append_failed decrement next_index=%d", peerID, n.nextIndex[peerID])
		}
		return
	}
	newMatch := prevIndex + uint64(len(entries))
	if newMatch > n.matchIndex[peerID] {
		n.matchIndex[peerID] = newMatch
	}
	n.nextIndex[peerID] = newMatch + 1
	n.advanceCommitLocked()
	n.stepf("replicate: <- peer=%s success match_index=%d next_index=%d", peerID, n.matchIndex[peerID], n.nextIndex[peerID])
}

func (n *Node) advanceCommitLocked() {
	prev := n.commitIndex
	for idx := n.lastLogIndex(); idx > n.commitIndex; idx-- {
		if int(idx) >= len(n.log) {
			continue
		}
		if n.log[idx].Term != n.currentTerm {
			break
		}
		count := 0
		for id := range n.peers {
			if n.matchIndex[id] >= idx {
				count++
			}
		}
		if count > len(n.peers)/2 {
			n.commitIndex = idx
			n.stepf("advance_commit: %d -> %d (index=%d term=%d quorum=%d/%d)",
				prev, n.commitIndex, idx, n.log[idx].Term, count, len(n.peers))
			return
		}
	}
}

func (n *Node) applyToLastApplied() {
	for n.lastApplied < n.commitIndex {
		n.lastApplied++
		if int(n.lastApplied) >= len(n.log) {
			return
		}
		e := n.log[n.lastApplied]
		switch e.Type {
		case kv.EntryConfig:
			p, err := kv.UnmarshalConfig(e.Data)
			if err == nil && len(p) > 0 {
				n.peers = clonePeers(p)
				for id := range n.peers {
					if _, ok := n.nextIndex[id]; !ok {
						n.nextIndex[id] = n.lastLogIndex() + 1
						n.matchIndex[id] = 0
					}
				}
				n.matchIndex[n.selfID] = n.lastLogIndex()
				n.stepf("apply: index=%d CONFIG peers=%d", n.lastApplied, len(n.peers))
			}
		case kv.EntrySet:
			var sp kv.SetPayload
			if json.Unmarshal(e.Data, &sp) == nil && sp.Key != "" {
				_ = n.st.SetKV(sp.Key, sp.Value)
				n.stepf("apply: index=%d SET key=%q", n.lastApplied, sp.Key)
			}
		case kv.EntryDelete:
			var dp kv.DeletePayload
			if json.Unmarshal(e.Data, &dp) == nil && dp.Key != "" {
				_ = n.st.DeleteKV(dp.Key)
				n.stepf("apply: index=%d DELETE key=%q", n.lastApplied, dp.Key)
			}
		default:
			n.stepf("apply: index=%d %s (noop/unknown)", n.lastApplied, entryTypeName(e.Type))
		}
	}
}

func (n *Node) peersFromLogLocked() (map[string]string, error) {
	for i := len(n.log) - 1; i >= 1; i-- {
		if n.log[i].Type == kv.EntryConfig {
			return kv.UnmarshalConfig(n.log[i].Data)
		}
	}
	return nil, errors.New("no config in log")
}

func (n *Node) Leader() (id, addr string, ok bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.leaderID == "" {
		return "", "", false
	}
	a, ok := n.peers[n.leaderID]
	return n.leaderID, a, ok
}

func (n *Node) IsLeader() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.role == RoleLeader
}

func (n *Node) ProposeConfigChange(peers map[string]string) error {
	n.mu.Lock()
	if n.role != RoleLeader {
		id, addr := n.leaderInfoLocked()
		n.stepf("propose CONFIG reject not_leader role=%s leader=%q at=%q", n.role.String(), id, addr)
		n.mu.Unlock()
		if id == "" {
			return fmt.Errorf("%w: no leader elected yet (need a majority of peers alive; a 2-node cluster cannot elect a new leader after the leader stops)", ErrNotLeader)
		}
		return fmt.Errorf("%w: leader is %q at %s", ErrNotLeader, id, addr)
	}
	data, err := kv.MarshalConfig(peers)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	idx := n.lastLogIndex() + 1
	n.log = append(n.log, storage.LogEntry{Index: idx, Term: n.currentTerm, Type: kv.EntryConfig, Data: data})
	if err := n.persistLocked(); err != nil {
		n.mu.Unlock()
		return err
	}
	n.matchIndex[n.selfID] = idx
	n.stepf("propose: CONFIG new_log_index=%d term=%d peer_count=%d", idx, n.currentTerm, len(peers))
	n.mu.Unlock()
	return n.waitCommit(context.Background(), idx)
}

func (n *Node) ProposeSet(key, value string) error {
	n.mu.Lock()
	if n.role != RoleLeader {
		id, addr := n.leaderInfoLocked()
		n.stepf("propose SET reject not_leader role=%s leader=%q at=%q key=%q", n.role.String(), id, addr, key)
		n.mu.Unlock()
		if id == "" {
			return fmt.Errorf("%w: no leader elected yet (need a majority of peers alive; a 2-node cluster cannot elect a new leader after the leader stops)", ErrNotLeader)
		}
		return fmt.Errorf("%w: leader is %q at %s", ErrNotLeader, id, addr)
	}
	data, err := kv.MarshalSet(key, value)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	idx := n.lastLogIndex() + 1
	n.log = append(n.log, storage.LogEntry{Index: idx, Term: n.currentTerm, Type: kv.EntrySet, Data: data})
	if err := n.persistLocked(); err != nil {
		n.mu.Unlock()
		return err
	}
	n.matchIndex[n.selfID] = idx
	n.stepf("propose: SET new_log_index=%d term=%d key=%q", idx, n.currentTerm, key)
	n.mu.Unlock()
	return n.waitCommit(context.Background(), idx)
}

func (n *Node) ProposeDelete(key string) error {
	n.mu.Lock()
	if n.role != RoleLeader {
		id, addr := n.leaderInfoLocked()
		n.stepf("propose DELETE reject not_leader role=%s leader=%q at=%q key=%q", n.role.String(), id, addr, key)
		n.mu.Unlock()
		if id == "" {
			return fmt.Errorf("%w: no leader elected yet (need a majority of peers alive; a 2-node cluster cannot elect a new leader after the leader stops)", ErrNotLeader)
		}
		return fmt.Errorf("%w: leader is %q at %s", ErrNotLeader, id, addr)
	}
	data, err := kv.MarshalDelete(key)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	idx := n.lastLogIndex() + 1
	n.log = append(n.log, storage.LogEntry{Index: idx, Term: n.currentTerm, Type: kv.EntryDelete, Data: data})
	if err := n.persistLocked(); err != nil {
		n.mu.Unlock()
		return err
	}
	n.matchIndex[n.selfID] = idx
	n.stepf("propose: DELETE new_log_index=%d term=%d key=%q", idx, n.currentTerm, key)
	n.mu.Unlock()
	return n.waitCommit(context.Background(), idx)
}

func (n *Node) leaderInfoLocked() (id, addr string) {
	if n.leaderID != "" {
		return n.leaderID, n.peers[n.leaderID]
	}
	return "", ""
}

func (n *Node) waitCommit(ctx context.Context, index uint64) error {
	deadline := time.Now().Add(8 * time.Second)
	round := 0
	for time.Now().Before(deadline) {
		round++
		n.replicateAllPeers(ctx)
		n.mu.Lock()
		if round == 1 || round%25 == 0 {
			n.stepf("wait_commit: want_index>=%d round=%d commit_idx=%d role=%s", index, round, n.commitIndex, n.role.String())
		}
		if n.commitIndex >= index {
			n.stepf("wait_commit: satisfied index=%d commit_idx=%d rounds=%d", index, n.commitIndex, round)
			n.mu.Unlock()
			return nil
		}
		n.advanceCommitLocked()
		n.mu.Unlock()
		time.Sleep(12 * time.Millisecond)
	}
	n.stepf("wait_commit: TIMEOUT wanted_index=%d", index)
	return errors.New("timeout waiting for commit")
}

func (n *Node) PeersSnapshot() (map[string]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return clonePeers(n.peers), nil
}

func (n *Node) Get(key string) (string, bool) {
	return n.st.GetKV(key)
}

func (n *Node) NodeID() string {
	return n.selfID
}
