package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file-backed ledger must survive a process restart: reopening the WAL
// replays the exact chain that was committed.
func TestWAL_PersistAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")

	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 3; i++ {
		mustAppend(t, l, i, "assign", `{"taskId":"t"}`)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen — simulates a fresh process reading the durable WAL.
	l2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer l2.Close()

	if l2.Len() != 3 {
		t.Fatalf("after replay want 3 entries, got %d", l2.Len())
	}
	if broken, err := l2.Verify(); broken != -1 || err != nil {
		t.Fatalf("replayed chain broken at %d: %v", broken, err)
	}

	// Appends continue from the replayed tail without breaking the chain.
	mustAppend(t, l2, 9, "assign", `{"taskId":"t3"}`)
	if broken, err := l2.Verify(); broken != -1 || err != nil {
		t.Fatalf("chain broke after post-replay append at %d: %v", broken, err)
	}
}

// A WAL tampered with on disk must be rejected at Open — recovery refuses to
// build state on a corrupted audit log.
func TestWAL_ReplayDetectsOnDiskTamper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")

	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustAppend(t, l, 0, "assign", `{"taskId":"t0"}`)
	mustAppend(t, l, 1, "assign", `{"taskId":"t1"}`)
	l.Close()

	// Forge the first record's payload on disk.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var e0 Entry
	if err := json.Unmarshal([]byte(lines[0]), &e0); err != nil {
		t.Fatal(err)
	}
	e0.Payload = json.RawMessage(`{"taskId":"FORGED"}`) // hash now stale
	forged, _ := json.Marshal(e0)
	lines[0] = string(forged)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("expected Open to reject a tampered WAL, got nil error")
	}
}

// An INTERIOR corrupt record (a complete, newline-terminated line that fails to
// parse) is real corruption/tampering and must be rejected.
func TestWAL_ReplayDetectsCorruptRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")
	if err := os.WriteFile(path, []byte("{not valid json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("expected Open to reject a corrupt WAL record, got nil error")
	}
}

// A torn TRAILING record (mid-write crash: bytes after the last newline) is an
// uncommitted write — it must be dropped, the file truncated to the last whole
// record, and every prior fsync'd record recovered. This is the most common
// crash shape and must NOT brick recovery.
func TestWAL_ReplayDropsTornTrailingRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")

	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustAppend(t, l, 0, "assign", `{"taskId":"t0"}`)
	mustAppend(t, l, 1, "assign", `{"taskId":"t1"}`)
	l.Close()

	// Simulate a crash mid-append: a partial JSON line with no trailing newline.
	raw, _ := os.ReadFile(path)
	torn := append(raw, []byte(`{"index":2,"kind":"assign","payl`)...) // truncated, no '\n'
	if err := os.WriteFile(path, torn, 0o644); err != nil {
		t.Fatal(err)
	}

	l2, err := Open(path)
	if err != nil {
		t.Fatalf("Open must tolerate a torn trailing record, got: %v", err)
	}
	defer l2.Close()
	if l2.Len() != 2 {
		t.Fatalf("want 2 recovered (prior fsync'd) entries, got %d", l2.Len())
	}
	if broken, err := l2.Verify(); broken != -1 || err != nil {
		t.Fatalf("recovered chain broken at %d: %v", broken, err)
	}

	// The torn tail must have been truncated, so a new append chains cleanly.
	mustAppend(t, l2, 9, "assign", `{"taskId":"t2"}`)
	if broken, err := l2.Verify(); broken != -1 || err != nil {
		t.Fatalf("chain broke after post-truncation append at %d: %v", broken, err)
	}

	// On-disk file must contain exactly 3 complete records now (no orphan bytes).
	raw2, _ := os.ReadFile(path)
	if got := strings.Count(string(raw2), "\n"); got != 3 {
		t.Fatalf("want 3 newline-terminated records on disk, got %d", got)
	}
}

// Two live ledgers over the same path must not both hold the write lock.
func TestWAL_SingleWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	if l2, err := Open(path); err == nil {
		l2.Close()
		t.Fatal("expected second Open to fail while the first holds the WAL lock")
	}
}
