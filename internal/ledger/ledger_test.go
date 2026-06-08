package ledger

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func ts(sec int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, sec, 0, time.UTC)
}

func mustAppend(t *testing.T, l *Ledger, sec int, kind string, payload string) Entry {
	t.Helper()
	e, err := l.Append(ts(sec), kind, json.RawMessage(payload))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return e
}

func TestAppendAndVerify_Intact(t *testing.T) {
	l := New()
	for i := 0; i < 5; i++ {
		mustAppend(t, l, i, "assign", fmt.Sprintf(`{"robotId":"r1","taskId":"t%d"}`, i))
	}
	if l.Len() != 5 {
		t.Fatalf("want 5 entries, got %d", l.Len())
	}
	if broken, err := l.Verify(); broken != -1 || err != nil {
		t.Fatalf("intact chain reported broken at %d: %v", broken, err)
	}
}

func TestChainLinksPrevHash(t *testing.T) {
	l := New()
	a := mustAppend(t, l, 0, "assign", `{}`)
	b := mustAppend(t, l, 1, "assign", `{}`)
	if a.PrevHash != GenesisPrevHash {
		t.Errorf("first entry prev-hash = %q, want genesis", a.PrevHash)
	}
	if b.PrevHash != a.Hash {
		t.Errorf("second entry not chained: prev=%q, want %q", b.PrevHash, a.Hash)
	}
}

// Tampering with a committed payload must be detected by Verify.
func TestVerify_DetectsTamper(t *testing.T) {
	l := New()
	mustAppend(t, l, 0, "assign", `{"taskId":"t0"}`)
	mustAppend(t, l, 1, "assign", `{"taskId":"t1"}`)
	mustAppend(t, l, 2, "assign", `{"taskId":"t2"}`)

	// Forge entry 1's payload directly in the backing slice.
	l.entries[1].Payload = json.RawMessage(`{"taskId":"FORGED"}`)

	broken, err := l.Verify()
	if err == nil {
		t.Fatal("expected tamper to be detected, got nil error")
	}
	if broken != 1 {
		t.Errorf("tamper detected at %d, want 1", broken)
	}
}

// Deleting/reordering an entry must break the prev-hash linkage.
func TestVerify_DetectsReorder(t *testing.T) {
	l := New()
	mustAppend(t, l, 0, "assign", `{"n":0}`)
	mustAppend(t, l, 1, "assign", `{"n":1}`)
	mustAppend(t, l, 2, "assign", `{"n":2}`)

	// Drop the middle entry — entry 2's PrevHash no longer matches its new predecessor.
	l.entries = append(l.entries[:1], l.entries[2])

	broken, err := l.Verify()
	if err == nil {
		t.Fatal("expected reorder/deletion to be detected, got nil error")
	}
	if broken != 1 {
		t.Errorf("break detected at %d, want 1", broken)
	}
}
