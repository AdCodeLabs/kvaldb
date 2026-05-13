package raft

import (
	"context"
	"testing"

	kvaldbpb "github.com/adcodelabs/kvaldb/internal/rpc"
)

type nopTransport struct{}

func (nopTransport) RequestVote(ctx context.Context, peerAddr string, req *kvaldbpb.RequestVoteRequest) (*kvaldbpb.RequestVoteResponse, error) {
	return &kvaldbpb.RequestVoteResponse{Term: req.Term, VoteGranted: true}, nil
}
func (nopTransport) AppendEntries(ctx context.Context, peerAddr string, req *kvaldbpb.AppendEntriesRequest) (*kvaldbpb.AppendEntriesResponse, error) {
	return &kvaldbpb.AppendEntriesResponse{Term: req.Term, Success: true}, nil
}

func TestAppendEntriesRejectStaleTerm(t *testing.T) {
	dir := t.TempDir()
	n, err := NewNode(Config{
		SelfID:       "a",
		SelfAddr:     "127.0.0.1:1",
		DataDir:      dir,
		Bootstrap:    true,
		Transport:    nopTransport{},
		InitialPeers: map[string]string{"a": "127.0.0.1:1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := n.AppendEntries(&kvaldbpb.AppendEntriesRequest{Term: 0, LeaderId: "x", LeaderCommit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatal("expected reject stale term")
	}
}

func TestRequestVoteRejectStaleTerm(t *testing.T) {
	dir := t.TempDir()
	n, err := NewNode(Config{
		SelfID:       "a",
		SelfAddr:     "127.0.0.1:1",
		DataDir:      dir,
		Bootstrap:    true,
		Transport:    nopTransport{},
		InitialPeers: map[string]string{"a": "127.0.0.1:1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := n.RequestVote(&kvaldbpb.RequestVoteRequest{Term: 0, CandidateId: "b", LastLogIndex: 0, LastLogTerm: 0})
	if err != nil {
		t.Fatal(err)
	}
	if resp.VoteGranted {
		t.Fatal("expected no vote for stale term")
	}
}
