package server

import (
	"context"
	"errors"
	"log"

	"github.com/adcodelabs/kvaldb/internal/raft"
	kvaldbpb "github.com/adcodelabs/kvaldb/internal/rpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func srvLogf(s *Service, format string, args ...any) {
	id := "?"
	if s != nil && s.Node != nil {
		id = s.Node.NodeID()
	}
	log.Printf("[grpc id=%s] "+format, append([]any{id}, args...)...)
}

type Service struct {
	kvaldbpb.UnimplementedRaftServer
	kvaldbpb.UnimplementedClusterServer
	kvaldbpb.UnimplementedKVServer

	Node *raft.Node
}

func (s *Service) RequestVote(ctx context.Context, req *kvaldbpb.RequestVoteRequest) (*kvaldbpb.RequestVoteResponse, error) {
	srvLogf(s, "rpc in RequestVote candidate=%s term=%d", req.CandidateId, req.Term)
	resp, err := s.Node.RequestVote(req)
	if err != nil {
		srvLogf(s, "rpc RequestVote error: %v", err)
		return nil, err
	}
	srvLogf(s, "rpc out RequestVote grant=%v term=%d", resp.VoteGranted, resp.Term)
	return resp, nil
}

func (s *Service) AppendEntries(ctx context.Context, req *kvaldbpb.AppendEntriesRequest) (*kvaldbpb.AppendEntriesResponse, error) {
	srvLogf(s, "rpc in AppendEntries leader=%s term=%d prev=(%d,%d) entries=%d",
		req.LeaderId, req.Term, req.PrevLogIndex, req.PrevLogTerm, len(req.Entries))
	resp, err := s.Node.AppendEntries(req)
	if err != nil {
		srvLogf(s, "rpc AppendEntries error: %v", err)
		return nil, err
	}
	srvLogf(s, "rpc out AppendEntries success=%v term=%d conflict_idx=%d", resp.Success, resp.Term, resp.ConflictIndex)
	return resp, nil
}

func (s *Service) Join(ctx context.Context, req *kvaldbpb.JoinRequest) (*kvaldbpb.JoinResponse, error) {
	srvLogf(s, "rpc in Join node_id=%s grpc_addr=%s", req.NodeId, req.GrpcAddr)
	if req.NodeId == "" || req.GrpcAddr == "" {
		return &kvaldbpb.JoinResponse{Ok: false, Error: "node_id and grpc_addr required"}, nil
	}
	if !s.Node.IsLeader() {
		id, addr, _ := s.Node.Leader()
		srvLogf(s, "rpc Join reject not_leader hint_leader=%s@%s", id, addr)
		return &kvaldbpb.JoinResponse{
			Ok:         false,
			Error:      "not leader",
			LeaderId:   id,
			LeaderAddr: addr,
		}, nil
	}
	peers, err := s.Node.PeersSnapshot()
	if err != nil {
		return &kvaldbpb.JoinResponse{Ok: false, Error: err.Error()}, nil
	}
	peers[req.NodeId] = req.GrpcAddr
	srvLogf(s, "rpc Join proposing new CONFIG total_peers=%d", len(peers))
	if err := s.Node.ProposeConfigChange(peers); err != nil {
		if errors.Is(err, raft.ErrNotLeader) {
			id, addr, _ := s.Node.Leader()
			return &kvaldbpb.JoinResponse{Ok: false, Error: err.Error(), LeaderId: id, LeaderAddr: addr}, nil
		}
		srvLogf(s, "rpc Join ProposeConfigChange error: %v", err)
		return &kvaldbpb.JoinResponse{Ok: false, Error: err.Error()}, nil
	}
	srvLogf(s, "rpc Join ok node=%s", req.NodeId)
	return &kvaldbpb.JoinResponse{Ok: true}, nil
}

func (s *Service) Metadata(ctx context.Context, _ *kvaldbpb.MetadataRequest) (*kvaldbpb.MetadataResponse, error) {
	st := s.Node.Status()
	srvLogf(s, "rpc in Metadata role=%s term=%d leader=%s", st.Role, st.CurrentTerm, st.LeaderID)
	var peers []*kvaldbpb.ClusterPeer
	for id, addr := range st.Peers {
		peers = append(peers, &kvaldbpb.ClusterPeer{NodeId: id, GrpcAddr: addr})
	}
	return &kvaldbpb.MetadataResponse{
		SelfId:       st.SelfID,
		SelfAddr:     st.SelfAddr,
		Role:         st.Role,
		CurrentTerm:  st.CurrentTerm,
		VotedFor:     st.VotedFor,
		LeaderId:     st.LeaderID,
		LeaderAddr:   st.LeaderAddr,
		CommitIndex:  st.CommitIndex,
		LastApplied:  st.LastApplied,
		LastLogIndex: st.LastLogIndex,
		LastLogTerm:  st.LastLogTerm,
		PeerCount:    int32(len(st.Peers)),
		Peers:        peers,
	}, nil
}

func (s *Service) Get(ctx context.Context, req *kvaldbpb.GetRequest) (*kvaldbpb.GetResponse, error) {
	if req.Key == "" {
		return nil, status.Error(codes.InvalidArgument, "empty key")
	}
	srvLogf(s, "rpc in KV.Get key=%q", req.Key)
	v, ok := s.Node.Get(req.Key)
	id, addr, _ := s.Node.Leader()
	if !ok {
		srvLogf(s, "rpc out KV.Get not_found key=%q", req.Key)
		return &kvaldbpb.GetResponse{Found: false, Error: "not found", LeaderId: id, LeaderAddr: addr}, nil
	}
	srvLogf(s, "rpc out KV.Get found key=%q", req.Key)
	return &kvaldbpb.GetResponse{Found: true, Value: v, LeaderId: id, LeaderAddr: addr}, nil
}

func (s *Service) Set(ctx context.Context, req *kvaldbpb.SetRequest) (*kvaldbpb.SetResponse, error) {
	if req.Key == "" {
		return nil, status.Error(codes.InvalidArgument, "empty key")
	}
	srvLogf(s, "rpc in KV.Set key=%q", req.Key)
	err := s.Node.ProposeSet(req.Key, req.Value)
	if err != nil {
		if errors.Is(err, raft.ErrNotLeader) {
			id, addr, _ := s.Node.Leader()
			srvLogf(s, "rpc out KV.Set not_leader leader_hint=%s@%s err=%v", id, addr, err)
			return &kvaldbpb.SetResponse{Ok: false, Error: err.Error(), LeaderId: id, LeaderAddr: addr}, nil
		}
		srvLogf(s, "rpc out KV.Set error: %v", err)
		return &kvaldbpb.SetResponse{Ok: false, Error: err.Error()}, nil
	}
	srvLogf(s, "rpc out KV.Set ok key=%q", req.Key)
	return &kvaldbpb.SetResponse{Ok: true}, nil
}

func (s *Service) Delete(ctx context.Context, req *kvaldbpb.DeleteRequest) (*kvaldbpb.DeleteResponse, error) {
	if req.Key == "" {
		return nil, status.Error(codes.InvalidArgument, "empty key")
	}
	srvLogf(s, "rpc in KV.Delete key=%q", req.Key)
	err := s.Node.ProposeDelete(req.Key)
	if err != nil {
		if errors.Is(err, raft.ErrNotLeader) {
			id, addr, _ := s.Node.Leader()
			srvLogf(s, "rpc out KV.Delete not_leader leader_hint=%s@%s err=%v", id, addr, err)
			return &kvaldbpb.DeleteResponse{Ok: false, Error: err.Error(), LeaderId: id, LeaderAddr: addr}, nil
		}
		srvLogf(s, "rpc out KV.Delete error: %v", err)
		return &kvaldbpb.DeleteResponse{Ok: false, Error: err.Error()}, nil
	}
	srvLogf(s, "rpc out KV.Delete ok key=%q", req.Key)
	return &kvaldbpb.DeleteResponse{Ok: true}, nil
}
