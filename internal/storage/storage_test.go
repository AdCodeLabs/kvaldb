package storage

import (
	"path/filepath"
	"testing"
)

func TestStoreMetaLogRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(Meta{CurrentTerm: 3, VotedFor: "a"}); err != nil {
		t.Fatal(err)
	}
	log := []LogEntry{
		{Index: 0, Term: 0, Type: 0, Data: nil},
		{Index: 1, Term: 1, Type: 2, Data: []byte(`{"key":"k","value":"v"}`)},
	}
	if err := s.SetLog(log); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := s2.GetMeta()
	if m.CurrentTerm != 3 || m.VotedFor != "a" {
		t.Fatalf("meta %+v", m)
	}
	lg, _ := s2.Log()
	if len(lg) != 2 || lg[1].Index != 1 {
		t.Fatalf("log %+v", lg)
	}
}

func TestStoreKVPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetKV("x", "y"); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := s2.GetKV("x")
	if !ok || v != "y" {
		t.Fatalf("got %q ok=%v", v, ok)
	}
}

func TestStoreOpenCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	_, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
}
