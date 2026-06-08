// Package ledger implements a tamper-evident, append-only audit log for the
// fleet controller. Every state-changing decision (task assignment, recovery,
// reassignment) is recorded as an Entry whose hash chains to its predecessor.
//
// This is demo ③ — the differentiator: any post-hoc edit, deletion, or
// reordering of an interior record breaks the chain and is detected by Verify.
//
// The ledger doubles as the durable write-ahead log (WAL) behind demo ② (crash
// recovery). Open replays an on-disk WAL and verifies its hash chain before
// accepting new appends. Crash semantics are explicit:
//
//   - A torn TRAILING record (the canonical mid-write power-loss shape) is
//     treated as an uncommitted write: it is dropped and the file is truncated
//     to the last complete record. Every prior fsync'd record still recovers.
//   - An interior parse/hash failure is real corruption or tampering and fails
//     Open hard.
//   - If Append's fsync fails the entry's durability is indeterminate, so the
//     ledger fails-stop (poisons itself) rather than risk index/chain drift.
package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// GenesisPrevHash is the prev-hash of the first entry (no predecessor).
const GenesisPrevHash = ""

// Entry is one immutable record in the audit chain.
type Entry struct {
	Index     uint64          `json:"index"`
	Timestamp time.Time       `json:"timestamp"`
	Kind      string          `json:"kind"`    // e.g. "assign", "recover", "reassign"
	Payload   json.RawMessage `json:"payload"` // domain event, opaque to the ledger
	PrevHash  string          `json:"prev_hash"`
	Hash      string          `json:"hash"` // = sha256(Index|Timestamp|Kind|Payload|PrevHash)
}

// Ledger is a concurrency-safe append-only chain. When backed by a file (via
// Open) every Append is durably persisted before it is acknowledged.
type Ledger struct {
	mu      sync.RWMutex
	entries []Entry
	file    *os.File // nil for in-memory ledgers created with New
	failed  error    // sticky: set when persistence fails, poisons further appends
}

// New returns an empty in-memory ledger (no persistence). Useful for tests.
func New() *Ledger { return &Ledger{} }

// Open opens (or creates) a file-backed, append-only ledger at path. Existing
// complete records are replayed and the hash chain is verified before the
// ledger accepts new appends; a torn trailing record is dropped (see package
// doc). The WAL is locked for single-writer access for the lifetime of the
// returned Ledger.
func Open(path string) (*Ledger, error) {
	l := &Ledger{}

	data, rerr := os.ReadFile(path)
	if rerr != nil && !os.IsNotExist(rerr) {
		return nil, fmt.Errorf("ledger: read %s: %w", path, rerr)
	}
	var validBytes int64
	if rerr == nil {
		vb, err := l.replay(data)
		if err != nil {
			return nil, err
		}
		validBytes = vb
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("ledger: open %s: %w", path, err)
	}

	// Single-writer guard: two processes appending to one WAL would interleave
	// records and shatter the hash chain.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("ledger: %s is locked by another process: %w", path, err)
	}

	// Drop any torn trailing bytes so subsequent O_APPEND writes chain cleanly.
	if validBytes < int64(len(data)) {
		if err := f.Truncate(validBytes); err != nil {
			f.Close()
			return nil, fmt.Errorf("ledger: truncate torn tail: %w", err)
		}
	}

	// A newly created file's directory entry is not durable until the directory
	// itself is fsync'd.
	if err := syncDir(path); err != nil {
		f.Close()
		return nil, fmt.Errorf("ledger: sync dir of %s: %w", path, err)
	}

	l.file = f
	return l, nil
}

// replay loads complete records from a WAL byte slice, verifies the chain, and
// returns the byte offset just past the last complete record. A torn trailing
// record (bytes after the final newline, or no newline at all) is reported via
// that offset so Open can truncate it; it is NOT an error. An interior record
// that fails to parse or verify IS an error. Runs during Open before any
// goroutine can touch the ledger, so it needs no lock.
func (l *Ledger) replay(data []byte) (validBytes int64, err error) {
	if len(data) == 0 {
		return 0, nil
	}
	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		// No complete (newline-terminated) record at all -> entire file is a
		// torn write. Recover nothing, signal full truncation.
		return 0, nil
	}

	body := data[:lastNL+1] // complete, newline-terminated records only
	for _, line := range bytes.Split(bytes.TrimRight(body, "\n"), []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return 0, fmt.Errorf("ledger: corrupt WAL record %d: %w", len(l.entries)+1, err)
		}
		l.entries = append(l.entries, e)
	}
	if broken, verr := l.verifyLocked(); verr != nil {
		return 0, fmt.Errorf("ledger: WAL integrity check failed at entry %d: %w", broken, verr)
	}
	return int64(lastNL + 1), nil
}

func syncDir(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// computeHash derives an entry's hash from its content + the previous hash.
// Hashing the canonical field order makes the chain reproducible and any
// mutation detectable.
func computeHash(index uint64, ts time.Time, kind string, payload json.RawMessage, prevHash string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|%s|", index, ts.UTC().Format(time.RFC3339Nano), kind)
	h.Write(payload)
	fmt.Fprintf(h, "|%s", prevHash)
	return hex.EncodeToString(h.Sum(nil))
}

// Append records a new event, durably persisting it (when file-backed) before
// it is added to the in-memory chain, then returns the committed entry. The
// caller supplies the timestamp so that replay is deterministic (no hidden
// clock).
//
// On any persistence failure the ledger is poisoned and rejects further
// appends: a half-written or unsynced record means the in-memory index and
// prev-hash can no longer be trusted to match disk, so failing-stop is the only
// safe choice. A returned error therefore means "not committed, outcome
// possibly indeterminate" — callers must treat it as fail-stop, not retry.
func (l *Ledger) Append(ts time.Time, kind string, payload json.RawMessage) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.failed != nil {
		return Entry{}, fmt.Errorf("ledger: poisoned by earlier persistence failure: %w", l.failed)
	}

	var prevHash string
	var index uint64
	if n := len(l.entries); n > 0 {
		prevHash = l.entries[n-1].Hash
		index = l.entries[n-1].Index + 1
	} else {
		prevHash = GenesisPrevHash
		index = 0
	}

	e := Entry{
		Index:     index,
		Timestamp: ts,
		Kind:      kind,
		Payload:   payload,
		PrevHash:  prevHash,
	}
	e.Hash = computeHash(e.Index, e.Timestamp, e.Kind, e.Payload, e.PrevHash)

	if l.file != nil {
		line, err := json.Marshal(e)
		if err != nil {
			return Entry{}, fmt.Errorf("ledger: marshal entry: %w", err)
		}
		if _, err := l.file.Write(append(line, '\n')); err != nil {
			l.failed = err
			return Entry{}, fmt.Errorf("ledger: append write: %w", err)
		}
		// fsync: the entry must survive a crash the instant Append returns.
		if err := l.file.Sync(); err != nil {
			l.failed = err // bytes may be on disk -> outcome indeterminate; fail-stop
			return Entry{}, fmt.Errorf("ledger: fsync: %w", err)
		}
	}

	l.entries = append(l.entries, e)
	return e, nil
}

// Close releases the file lock and handle (if any). Safe on in-memory ledgers.
func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		err := l.file.Close() // also releases the flock
		l.file = nil
		return err
	}
	return nil
}

// Entries returns a defensive copy of the full chain.
func (l *Ledger) Entries() []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Entry, len(l.entries))
	copy(out, l.entries)
	return out
}

// Len reports the number of entries.
func (l *Ledger) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.entries)
}

// Verify walks the chain and confirms (1) each entry's hash matches its
// recomputed content, and (2) each entry's PrevHash equals the prior entry's
// Hash. It returns the index of the first broken entry, or -1 if intact.
func (l *Ledger) Verify() (brokenAt int, err error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.verifyLocked()
}

func (l *Ledger) verifyLocked() (brokenAt int, err error) {
	prevHash := GenesisPrevHash
	for i, e := range l.entries {
		if e.PrevHash != prevHash {
			return i, fmt.Errorf("entry %d: prev-hash mismatch (chain broken or reordered)", i)
		}
		want := computeHash(e.Index, e.Timestamp, e.Kind, e.Payload, e.PrevHash)
		if want != e.Hash {
			return i, fmt.Errorf("entry %d: content tampered (hash mismatch)", i)
		}
		prevHash = e.Hash
	}
	return -1, nil
}
