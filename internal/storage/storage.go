package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Meta struct {
	CurrentTerm uint64 `json:"current_term"`
	VotedFor    string `json:"voted_for"`
}

type LogEntry struct {
	Index uint64 `json:"index"`
	Term  uint64 `json:"term"`
	Type  uint32 `json:"type"`
	Data  []byte `json:"data"`
}

const (
	metaFileName = "meta.json"
	logFileName  = "log.json"
	kvFileName   = "kv.json"
)

type Store struct {
	mu       sync.Mutex
	dir      string
	meta     Meta
	log      []LogEntry // log[0] is sentinel {Index:0, Term:0}
	kv       map[string]string
	dirtyLog bool
	dirtyKV  bool
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir: dir,
		log: []LogEntry{{Index: 0, Term: 0, Type: 0, Data: nil}},
		kv:  make(map[string]string),
	}
	if err := s.loadMeta(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := s.loadLog(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := s.loadKV(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, name)
}

func (s *Store) loadMeta() error {
	b, err := os.ReadFile(s.path(metaFileName))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.meta)
}

func (s *Store) loadLog() error {
	b, err := os.ReadFile(s.path(logFileName))
	if err != nil {
		return err
	}
	var entries []LogEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return err
	}
	if len(entries) == 0 {
		s.log = []LogEntry{{Index: 0, Term: 0, Type: 0, Data: nil}}
		return nil
	}
	// Ensure sentinel at index 0.
	if entries[0].Index != 0 {
		return fmt.Errorf("storage: invalid log: first entry index %d", entries[0].Index)
	}
	s.log = entries
	return nil
}

func (s *Store) loadKV() error {
	b, err := os.ReadFile(s.path(kvFileName))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.kv)
}

func (s *Store) flushMetaLocked() error {
	b, err := json.MarshalIndent(&s.meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(metaFileName) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(metaFileName))
}

func (s *Store) flushLogLocked() error {
	b, err := json.MarshalIndent(s.log, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(logFileName) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(logFileName))
}

func (s *Store) flushKVLocked() error {
	b, err := json.MarshalIndent(s.kv, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(kvFileName) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(kvFileName))
}

func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.flushMetaLocked(); err != nil {
		return err
	}
	if s.dirtyLog {
		if err := s.flushLogLocked(); err != nil {
			return err
		}
		s.dirtyLog = false
	}
	if s.dirtyKV {
		if err := s.flushKVLocked(); err != nil {
			return err
		}
		s.dirtyKV = false
	}
	return nil
}

func (s *Store) GetMeta() (Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta, nil
}

func (s *Store) SetMeta(m Meta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta = m
	return s.flushMetaLocked()
}

func (s *Store) Log() ([]LogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LogEntry, len(s.log))
	copy(out, s.log)
	return out, nil
}

func (s *Store) SetLog(entries []LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(entries) == 0 || entries[0].Index != 0 {
		return errors.New("storage: SetLog requires sentinel at index 0")
	}
	s.log = make([]LogEntry, len(entries))
	copy(s.log, entries)
	s.dirtyLog = true
	return s.flushLogLocked()
}

func (s *Store) AppendEntries(truncateAfter uint64, newEntries []LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := make([]LogEntry, 0, len(s.log))
	for _, e := range s.log {
		if e.Index <= truncateAfter {
			keep = append(keep, e)
		}
	}
	s.log = keep
	s.log = append(s.log, newEntries...)
	s.dirtyLog = true
	return s.flushLogLocked()
}

func (s *Store) GetKV(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.kv[key]
	return v, ok
}

func (s *Store) SetKV(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[key] = value
	s.dirtyKV = true
	return s.flushKVLocked()
}

func (s *Store) DeleteKV(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.kv, key)
	s.dirtyKV = true
	return s.flushKVLocked()
}

func (s *Store) ReplaceKV(m map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv = make(map[string]string)
	for k, v := range m {
		s.kv[k] = v
	}
	s.dirtyKV = true
	return s.flushKVLocked()
}

func (s *Store) SnapshotKV() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.kv))
	for k, v := range s.kv {
		out[k] = v
	}
	return out
}
