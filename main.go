package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adcodelabs/kvaldb/internal/client"
	"github.com/adcodelabs/kvaldb/internal/raft"
	"github.com/adcodelabs/kvaldb/internal/server"
	"google.golang.org/grpc"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "node":
		if err := runNode(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "client":
		if err := runClient(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `kvaldb — educational distributed KV with scratch Raft

Usage:
  %s node   --id <id> --addr <host:port> --data <dir> [--bootstrap] [--join <leader_host:port>]
  %s client --addr <host:port> get <key>
  %s client --addr <host:port> set <key> <value>
  %s client --addr <host:port> del <key>
  %s client --addr <host:port> meta
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}

func runNode(args []string) error {
	fs := flag.NewFlagSet("node", flag.ExitOnError)
	id := fs.String("id", "n1", "unique node id in the cluster")
	addr := fs.String("addr", "127.0.0.1:9001", "gRPC listen address (host:port)")
	data := fs.String("data", "./data/node", "directory for Raft log and KV files")
	bootstrap := fs.Bool("bootstrap", false, "bootstrap a new single-node cluster")
	join := fs.String("join", "", "gRPC address of an existing node to request membership")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dialer := server.NewDialer()
	defer dialer.Close()

	transport := server.NewGRPCRaftTransport(dialer, *id, *addr)
	log.Printf("[node] creating raft node id=%q addr=%q data=%q bootstrap=%v join=%q", *id, *addr, *data, *bootstrap, *join)
	node, err := raft.NewNode(raft.Config{
		SelfID:    *id,
		SelfAddr:  *addr,
		DataDir:   *data,
		Bootstrap: *bootstrap,
		Transport: transport,
	})
	if err != nil {
		return err
	}
	log.Printf("[node] raft node created id=%q", *id)

	svc := &server.Service{Node: node}
	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	gs := grpc.NewServer()
	server.Register(gs, svc)

	go func() {
		log.Printf("[node] gRPC listening id=%q addr=%s", *id, *addr)
		if err := gs.Serve(lis); err != nil {
			log.Printf("grpc serve: %v", err)
		}
	}()

	node.Start()
	log.Printf("[node] raft background loops started id=%q", *id)

	if *join != "" {
		go func() {
			log.Printf("[node] join worker started id=%q target=%q", *id, *join)
			time.Sleep(300 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			leader := *join
			for attempt := 0; attempt < 8; attempt++ {
				resp, err := client.Join(ctx, leader, *id, *addr)
				if err != nil {
					log.Printf("join attempt %d: %v", attempt+1, err)
					time.Sleep(500 * time.Millisecond)
					continue
				}
				if resp.Ok {
					log.Printf("[node] join OK id=%q via_leader=%s", *id, leader)
					return
				}
				if resp.LeaderAddr != "" {
					log.Printf("[node] join redirect id=%q new_leader_addr=%s", *id, resp.LeaderAddr)
					leader = resp.LeaderAddr
					continue
				}
				log.Printf("[node] join failed id=%q err=%s", *id, resp.Error)
				time.Sleep(500 * time.Millisecond)
			}
			log.Printf("[node] join giving up id=%q", *id)
		}()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	log.Printf("[node] running id=%q (Ctrl+C to stop)", *id)
	<-sig
	log.Printf("[node] shutdown signal id=%q stopping gRPC and raft", *id)
	gs.GracefulStop()
	node.Stop()
	return nil
}

func runClient(args []string) error {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9001", "target node gRPC address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return fmt.Errorf("client: need command: get|set|del|meta")
	}
	cmd := rest[0]
	switch cmd {
	case "get":
		if len(rest) != 2 {
			return fmt.Errorf("client: get <key>")
		}
		return client.RunGet(*addr, rest[1])
	case "set":
		if len(rest) != 3 {
			return fmt.Errorf("client: set <key> <value>")
		}
		return client.RunSet(*addr, rest[1], rest[2])
	case "del", "delete":
		if len(rest) != 2 {
			return fmt.Errorf("client: del <key>")
		}
		return client.RunDelete(*addr, rest[1])
	case "meta", "metadata", "status":
		if len(rest) != 1 {
			return fmt.Errorf("client: meta (no extra arguments)")
		}
		return client.RunMetadata(*addr)
	default:
		return fmt.Errorf("unknown client command %q", cmd)
	}
}
