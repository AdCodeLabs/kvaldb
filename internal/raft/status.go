package raft

type NodeStatus struct {
	SelfID       string
	SelfAddr     string
	Role         string
	CurrentTerm  uint64
	VotedFor     string
	LeaderID     string
	LeaderAddr   string
	CommitIndex  uint64
	LastApplied  uint64
	LastLogIndex uint64
	LastLogTerm  uint64
	Peers        map[string]string
}

func (n *Node) Status() NodeStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	leaderID, leaderAddr := n.leaderInfoLocked()
	return NodeStatus{
		SelfID:       n.selfID,
		SelfAddr:     n.selfAddr,
		Role:         n.role.String(),
		CurrentTerm:  n.currentTerm,
		VotedFor:     n.votedFor,
		LeaderID:     leaderID,
		LeaderAddr:   leaderAddr,
		CommitIndex:  n.commitIndex,
		LastApplied:  n.lastApplied,
		LastLogIndex: n.lastLogIndex(),
		LastLogTerm:  n.lastLogTerm(),
		Peers:        clonePeers(n.peers),
	}
}
