package raft

import (
	"fmt"
	"log"

	"github.com/adcodelabs/kvaldb/internal/kv"
)

func entryTypeName(t uint32) string {
	switch t {
	case kv.EntryNoop:
		return "noop"
	case kv.EntryConfig:
		return "config"
	case kv.EntrySet:
		return "set"
	case kv.EntryDelete:
		return "delete"
	default:
		return fmt.Sprintf("type=%d", t)
	}
}

func (n *Node) stepf(format string, args ...any) {
	log.Printf("[raft id=%s addr=%s] "+format, append([]any{n.selfID, n.selfAddr}, args...)...)
}
