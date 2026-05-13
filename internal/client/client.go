package client

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	kvaldbpb "github.com/adcodelabs/kvaldb/internal/rpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func RunGet(addr, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cur := addr
	for attempt := 0; attempt < 3; attempt++ {
		conn, err := grpc.NewClient(cur, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return err
		}
		cli := kvaldbpb.NewKVClient(conn)
		resp, err := cli.Get(ctx, &kvaldbpb.GetRequest{Key: key})
		_ = conn.Close()
		if err != nil {
			return err
		}
		if resp.Found {
			fmt.Println(resp.Value)
			return nil
		}
		if resp.LeaderAddr != "" && resp.LeaderAddr != cur {
			cur = resp.LeaderAddr
			continue
		}
		if resp.Error != "" && resp.Error != "not found" {
			fmt.Println(resp.Error)
			return nil
		}
		fmt.Printf("(not found) key=%q\n", key)
		return nil
	}
	return errors.New("too many redirects")
}

func RunSet(addr, key, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return tryKVWrite(ctx, addr, func(ctx context.Context, c kvaldbpb.KVClient) (ok bool, leaderAddr string, err error) {
		resp, err := c.Set(ctx, &kvaldbpb.SetRequest{Key: key, Value: value})
		if err != nil {
			return false, "", err
		}
		if resp.Ok {
			return true, "", nil
		}
		return false, resp.LeaderAddr, nil
	})
}

func RunDelete(addr, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return tryKVWrite(ctx, addr, func(ctx context.Context, c kvaldbpb.KVClient) (ok bool, leaderAddr string, err error) {
		resp, err := c.Delete(ctx, &kvaldbpb.DeleteRequest{Key: key})
		if err != nil {
			return false, "", err
		}
		if resp.Ok {
			return true, "", nil
		}
		return false, resp.LeaderAddr, nil
	})
}

// returns the gRPC address of the current cluster leader from a node's Metadata
func fetchLeaderAddr(ctx context.Context, nodeGRPCAddr string) (string, error) {
	conn, err := grpc.NewClient(nodeGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	md, err := kvaldbpb.NewClusterClient(conn).Metadata(ctx, &kvaldbpb.MetadataRequest{})
	if err != nil {
		return "", err
	}
	return md.GetLeaderAddr(), nil
}

// retries writes until success: follows leader from KV response, then polls Metadata while leader is unknown
func tryKVWrite(ctx context.Context, dialAddr string, fn func(context.Context, kvaldbpb.KVClient) (ok bool, leaderAddr string, err error)) error {
	cur := dialAddr
	for i := 0; i < 100; i++ {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("%w: no leader became reachable in time — if you had exactly 2 nodes and stopped the leader, Raft cannot elect a new one (need a strict majority); use at least 3 nodes for one failure tolerance", err)
			}
			return err
		}
		conn, err := grpc.NewClient(cur, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		cli := kvaldbpb.NewKVClient(conn)
		ok, hint, err := fn(ctx, cli)
		_ = conn.Close()
		if err != nil {
			return err
		}
		if ok {
			fmt.Println("ok")
			return nil
		}
		if hint != "" && hint != cur {
			cur = hint
			continue
		}
		la, _ := fetchLeaderAddr(ctx, cur)
		if la != "" && la != cur {
			cur = la
			continue
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%w: still no leader — with 2 nodes, losing the leader leaves no quorum; run 3+ nodes or keep the leader up", ctx.Err())
			}
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return fmt.Errorf("gave up waiting for leader: a 2-node cluster cannot elect a new leader after the leader dies (Raft needs majority); use 3+ nodes for HA")
}

func Join(ctx context.Context, leaderAddr, nodeID, grpcAddr string) (*kvaldbpb.JoinResponse, error) {
	conn, err := grpc.NewClient(leaderAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	cli := kvaldbpb.NewClusterClient(conn)
	return cli.Join(ctx, &kvaldbpb.JoinRequest{NodeId: nodeID, GrpcAddr: grpcAddr})
}

func RunMetadata(addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	cli := kvaldbpb.NewClusterClient(conn)
	resp, err := cli.Metadata(ctx, &kvaldbpb.MetadataRequest{})
	if err != nil {
		return err
	}
	fmt.Printf("self:        %s @ %s\n", resp.SelfId, resp.SelfAddr)
	fmt.Printf("role:        %s\n", resp.Role)
	fmt.Printf("term:        %d\n", resp.CurrentTerm)
	fmt.Printf("voted_for:   %q\n", resp.VotedFor)
	if resp.LeaderId != "" {
		fmt.Printf("leader:      %s @ %s\n", resp.LeaderId, resp.LeaderAddr)
	} else {
		fmt.Printf("leader:      (none / unknown)\n")
	}
	fmt.Printf("commit_idx:  %d\n", resp.CommitIndex)
	fmt.Printf("applied_idx: %d\n", resp.LastApplied)
	fmt.Printf("last_log:    index=%d term=%d\n", resp.LastLogIndex, resp.LastLogTerm)
	fmt.Printf("peers (%d):\n", resp.PeerCount)
	ids := make([]string, 0, len(resp.Peers))
	byID := make(map[string]string)
	for _, p := range resp.Peers {
		ids = append(ids, p.NodeId)
		byID[p.NodeId] = p.GrpcAddr
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("  - %s @ %s\n", id, byID[id])
	}
	return nil
}
