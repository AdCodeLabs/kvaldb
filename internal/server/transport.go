package server

import (
	"context"
	"log"

	"github.com/adcodelabs/kvaldb/internal/raft"
	kvaldbpb "github.com/adcodelabs/kvaldb/internal/rpc"
	"google.golang.org/grpc"
)

type GRPCRaftTransport struct {
	d      *Dialer
	nodeID string
}

func NewGRPCRaftTransport(d *Dialer, nodeID, _ string) raft.PeerTransport {
	return &GRPCRaftTransport{d: d, nodeID: nodeID}
}

func (t *GRPCRaftTransport) RequestVote(ctx context.Context, peerAddr string, req *kvaldbpb.RequestVoteRequest) (*kvaldbpb.RequestVoteResponse, error) {
	log.Printf("[transport id=%s] out RequestVote -> %s term=%d candidate=%s", t.nodeID, peerAddr, req.Term, req.CandidateId)
	conn, err := t.d.Conn(peerAddr)
	if err != nil {
		log.Printf("[transport id=%s] RequestVote dial %s: %v", t.nodeID, peerAddr, err)
		return nil, err
	}
	cli := kvaldbpb.NewRaftClient(conn)
	resp, err := cli.RequestVote(ctx, req)
	if err != nil {
		log.Printf("[transport id=%s] RequestVote <- %s err=%v", t.nodeID, peerAddr, err)
		return nil, err
	}
	log.Printf("[transport id=%s] RequestVote <- %s grant=%v term=%d", t.nodeID, peerAddr, resp.VoteGranted, resp.Term)
	return resp, nil
}

func (t *GRPCRaftTransport) AppendEntries(ctx context.Context, peerAddr string, req *kvaldbpb.AppendEntriesRequest) (*kvaldbpb.AppendEntriesResponse, error) {
	log.Printf("[transport id=%s] out AppendEntries -> %s leader=%s term=%d prev=(%d,%d) entries=%d",
		t.nodeID, peerAddr, req.LeaderId, req.Term, req.PrevLogIndex, req.PrevLogTerm, len(req.Entries))
	conn, err := t.d.Conn(peerAddr)
	if err != nil {
		log.Printf("[transport id=%s] AppendEntries dial %s: %v", t.nodeID, peerAddr, err)
		return nil, err
	}
	cli := kvaldbpb.NewRaftClient(conn)
	resp, err := cli.AppendEntries(ctx, req)
	if err != nil {
		log.Printf("[transport id=%s] AppendEntries <- %s err=%v", t.nodeID, peerAddr, err)
		return nil, err
	}
	log.Printf("[transport id=%s] AppendEntries <- %s success=%v term=%d conflict=%d", t.nodeID, peerAddr, resp.Success, resp.Term, resp.ConflictIndex)
	return resp, nil
}

// Register registers all gRPC services on the given server.
func Register(grpcSrv *grpc.Server, srv *Service) {
	kvaldbpb.RegisterRaftServer(grpcSrv, srv)
	kvaldbpb.RegisterClusterServer(grpcSrv, srv)
	kvaldbpb.RegisterKVServer(grpcSrv, srv)
}
