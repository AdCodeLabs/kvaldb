# kvaldb — Distributed key-value store with Raft

**kvaldb** is an educational project: a replicated key-value database where **Raft consensus is implemented in Go without using a Raft library**, persistence uses **simple JSON files** (no BoltDB or similar), and nodes/clients communicate over **gRPC**.

It prioritizes clarity and learning over production robustness.

---

## Table of contents

1. [What problem this solves](#what-problem-this-solves)
2. [Raft concepts (short course)](#raft-concepts-short-course)
3. [Architecture of this codebase](#architecture-of-this-codebase)
4. [How a write travels through the system](#how-a-write-travels-through-the-system)
5. [Build and run](#build-and-run)
6. [Running a three-node cluster](#running-a-three-node-cluster)
7. [Client CLI](#client-cli)
8. [Failure demos](#failure-demos)
9. [Regenerating protobuf code](#regenerating-protobuf-code)
10. [Limitations and honesty box](#limitations-and-honesty-box)

---

## What problem this solves

In a distributed system, multiple machines can fail or lag. **Consensus** lets a group of servers agree on a single ordered log of operations. If a majority (quorum) agrees, the operation is **committed** and can be applied to a **state machine** — here, an in-memory map persisted as JSON.

**Raft** is a consensus algorithm designed to be understandable: at any time there is at most one **leader** that accepts new log entries and replicates them to **followers**.

---

## Raft concepts (short course)

| Concept | Meaning |
|--------|---------|
| **Leader** | Accepts client writes, appends to its log, replicates to followers, advances **commit index** when a quorum has stored the entry. |
| **Follower** | Passive replica: votes in elections, receives **AppendEntries** from the leader, applies committed entries. |
| **Candidate** | Temporary role during an **election**: increments **term**, votes for itself, asks others for **RequestVote**. |
| **Term** | Logical clock (monotonic). Higher term always wins; split votes trigger a new term and election. |
| **Log** | Ordered entries `(index, term, type, data)`. Matching `prevLogIndex` / `prevLogTerm` ensures logs stay consistent. |
| **Commit index** | Highest log index known to be safe to apply on the state machine (KV store). |
| **Quorum** | Strict majority of configured servers, e.g. 2 of 3. Commits require quorum replication. |
| **Election timeout** | Follower starts an election if it hears no valid AppendEntries / votes in time (randomized to reduce split votes). |
| **Configuration (membership)** | A special log entry type **CONFIG** stores the map `nodeId → gRPC address`. Joining nodes trigger a new CONFIG on the leader. |

This implementation also uses **quiescent joiners**: a node that has not yet received replication from a leader does **not** start elections while it only knows itself, avoiding an incorrect self-election before the first **AppendEntries** arrives.

---

## Architecture of this codebase

```text
main.go                 # CLI entry: `node` and `client` subcommands
proto/kvaldb.proto      # gRPC: Raft RPCs, Cluster.Join, KV
internal/rpc/           # Generated `*.pb.go` (protoc)
internal/storage/       # meta.json, log.json, kv.json
internal/kv/            # Command payloads (CONFIG / SET / DELETE) JSON encoding
internal/raft/          # Raft core: elections, append entries, commit, apply
internal/server/        # gRPC services + peer dial cache + transport
internal/client/        # CLI client: get/set/del with leader redirect
```

**gRPC services**

- **Raft** — `RequestVote`, `AppendEntries` (node-to-node).
- **Cluster** — `Join` (dynamic membership; only the leader appends a CONFIG entry), `Metadata` (read-only snapshot of local Raft view: leader, term, role, peers, indices).
- **KV** — `Get`, `Set`, `Delete` (client-facing; writes only on leader).

**Persistence**

- `meta.json` — `current_term`, `voted_for`.
- `log.json` — full replicated log as a JSON array (index `0` is a sentinel entry).
- `kv.json` — applied key-value map after committed commands.

---

## How a write travels through the system

1. Client sends **Set** to any node over gRPC.
2. If the node is not the leader, it responds with **not leader** and optional **leader address**; the client retries on the leader.
3. The leader appends a **SET** log entry at the current **term**, persists the log, and sends **AppendEntries** to followers (heartbeats carry commit hints).
4. When a **quorum** has replicated the entry and Raft safety rules allow, the leader raises **commitIndex**.
5. Each node’s **applier** loop applies new committed entries: CONFIG updates the peer map; SET/DELETE update `kv.json`.

Reads (**Get**) are served from the local applied `kv.json` for simplicity; they may be stale on followers until replication catches up (documented limitation).

---

## Build and run

Requirements: **Go 1.22+**, **protobuf compiler** (`protoc`) only if you change `.proto` files.

```bash
go build -o kvaldb .
./kvaldb node --help   # (flags are standard library flag; see usage in main)
```

Print usage:

```bash
go run . 2>&1 | head
```

---

## Running a three-node cluster

Use three terminals and **three different data directories**. Ports must be distinct.

**Terminal 1 — bootstrap leader**

```bash
go run . node --id n1 --addr 127.0.0.1:9001 --data ./data/n1 --bootstrap
```

**Terminal 2 — join second node**

```bash
go run . node --id n2 --addr 127.0.0.1:9002 --data ./data/n2 --join 127.0.0.1:9001
```

**Terminal 3 — join third node**

```bash
go run . node --id n3 --addr 127.0.0.1:9003 --data ./data/n3 --join 127.0.0.1:9001
```

Wait until join logs show success (leader processes `Join` and replicates CONFIG). A **3-node** cluster tolerates **one** failed node for commits (quorum = 2).

---

## Client CLI

```bash
# Write (goes to leader; client may auto-retry on follower)
go run . client --addr 127.0.0.1:9001 set name Alice

# Read
go run . client --addr 127.0.0.1:9002 get name

# Delete
go run . client --addr 127.0.0.1:9003 del name

# Cluster / Raft metadata (leader id and address, term, role, commit indices, peer list)
go run . client --addr 127.0.0.1:9002 meta
```

Aliases: `metadata` and `status` behave the same as `meta`.

Expected:

- `set` / `del` print `ok` on success.
- `get` prints the value or `(not found) key="..."`.
- `meta` prints a short text report (who this node is, current Raft role, term, known **leader** / master, log positions, configured peers).

---

## Failure demos

1. **Kill the leader process** (Ctrl+C on its terminal). With **three or more** nodes, the rest run an election and a new leader should appear after an election timeout. With **exactly two** nodes, the survivor **cannot** form a majority, so **no new leader is ever elected** and writes fail until you restart the old leader or add a third node.
2. **Restart a stopped node** with the same `--data` directory: it reloads `meta.json`, `log.json`, and `kv.json` and rejoins replication when the leader sends AppendEntries.
3. **Stop a follower** — with 3 nodes, writes can still commit; with 2 nodes, both must be alive for a quorum of 2.

### Why `client set` sometimes shows `no leader` / empty leader

Raft needs a **strict majority** of peers in the latest configuration. A **2-node** cluster has quorum size 2; if one process stops, the other can never win an election. The `client` now **polls** `Metadata` for a while when the leader is unknown (covers brief election gaps on 3+ nodes), then returns an error that mentions the 2-node case.

---

## Regenerating protobuf code

From the repository root (with `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` on `PATH`):

```bash
protoc --go_out=. --go_opt=module=github.com/adcodelabs/kvaldb \
  --go-grpc_out=. --go-grpc_opt=module=github.com/adcodelabs/kvaldb \
  -I proto proto/kvaldb.proto
```

Pinned plugin versions compatible with Go 1.22 are fine; see `go.mod` for gRPC/protobuf versions.
