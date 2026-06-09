package reconcile_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/arti1117/fleet-master-controller/internal/event"
	"github.com/arti1117/fleet-master-controller/internal/ledger"
	"github.com/arti1117/fleet-master-controller/internal/reconcile"
)

func ts(sec int) time.Time { return time.Date(2026, 1, 1, 0, 0, sec, 0, time.UTC) }

// builder appends order/state/assign events to an in-memory ledger.
type builder struct {
	t   *testing.T
	l   *ledger.Ledger
	sec int
}

func newBuilder(t *testing.T) *builder { return &builder{t: t, l: ledger.New()} }

func (b *builder) append(kind string, payload any) {
	b.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		b.t.Fatal(err)
	}
	if _, err := b.l.Append(ts(b.sec), kind, raw); err != nil {
		b.t.Fatal(err)
	}
	b.sec++
}

func (b *builder) issue(robot, order string, upd uint32) {
	b.append(event.KindOrderIssued, event.OrderIssued{RobotID: robot, OrderID: order, OrderUpdateID: upd})
}

func (b *builder) report(robot, order string, upd uint32, driving bool) {
	b.append(event.KindStateReported, event.StateReported{RobotID: robot, OrderID: order, OrderUpdateID: upd, Driving: driving})
}

func (b *builder) reconcile() []reconcile.Finding {
	b.t.Helper()
	f, err := reconcile.Reconcile(b.l.Entries())
	if err != nil {
		b.t.Fatalf("Reconcile: %v", err)
	}
	return f
}

func only(t *testing.T, f []reconcile.Finding) reconcile.Finding {
	t.Helper()
	if len(f) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(f), f)
	}
	return f[0]
}

func TestReconcile_Accepted(t *testing.T) {
	b := newBuilder(t)
	b.issue("agv-01", "ORD", 1)
	b.report("agv-01", "ORD", 1, false)
	if got := only(t, b.reconcile()).Status; got != reconcile.StatusAccepted {
		t.Fatalf("status = %s, want ACCEPTED", got)
	}
}

func TestReconcile_AcceptedViaNewerUpdate(t *testing.T) {
	b := newBuilder(t)
	b.issue("agv-01", "ORD", 1)
	b.report("agv-01", "ORD", 2, true) // robot already on a newer update
	if got := only(t, b.reconcile()).Status; got != reconcile.StatusAccepted {
		t.Fatalf("status = %s, want ACCEPTED", got)
	}
}

func TestReconcile_Pending(t *testing.T) {
	b := newBuilder(t)
	b.issue("agv-01", "ORD", 2)
	b.report("agv-01", "ORD", 1, true) // behind, but still driving
	if got := only(t, b.reconcile()).Status; got != reconcile.StatusPending {
		t.Fatalf("status = %s, want PENDING", got)
	}
}

// Behind the issued update and not driving (last report) => STALLED: the issued
// update is UNCONFIRMED. We deliberately do NOT claim "rejected" — driving=false
// is non-terminal in VDA5050 (a mid-order pause looks identical from the log).
func TestReconcile_Stalled(t *testing.T) {
	b := newBuilder(t)
	b.issue("agv-01", "ORD", 2)
	b.report("agv-01", "ORD", 1, false) // behind and not driving as of the last state
	f := only(t, b.reconcile())
	if f.Status != reconcile.StatusStalled {
		t.Fatalf("status = %s, want STALLED", f.Status)
	}
	if f.IssuedUpdateID != 2 || f.ObservedUpdateID != 1 {
		t.Fatalf("issued/observed = %d/%d, want 2/1", f.IssuedUpdateID, f.ObservedUpdateID)
	}
}

func TestReconcile_Unobserved(t *testing.T) {
	b := newBuilder(t)
	b.issue("agv-01", "ORD", 1) // no state ever references this order
	if got := only(t, b.reconcile()).Status; got != reconcile.StatusUnobserved {
		t.Fatalf("status = %s, want UNOBSERVED", got)
	}
}

// The verdict, ObservedUpdateID, and Detail must all come from the SAME (latest)
// report — never a high-water id decoupled from the latest driving flag.
func TestReconcile_UsesLatestReportNotHighWater(t *testing.T) {
	b := newBuilder(t)
	b.issue("agv-01", "ORD", 5)
	b.report("agv-01", "ORD", 3, true)  // earlier: higher id, driving
	b.report("agv-01", "ORD", 2, false) // latest: lower id, stopped
	f := only(t, b.reconcile())
	if f.Status != reconcile.StatusStalled {
		t.Fatalf("status = %s, want STALLED (latest report drives the verdict)", f.Status)
	}
	if f.ObservedUpdateID != 2 {
		t.Fatalf("ObservedUpdateID = %d, want 2 (the latest report, not high-water 3)", f.ObservedUpdateID)
	}
}

// An orphan state (a report for an order the controller never issued) is out of
// scope for this unit: it must produce no finding (pins the documented boundary).
func TestReconcile_OrphanStateIgnored(t *testing.T) {
	b := newBuilder(t)
	b.report("agv-01", "GHOST", 1, false) // never issued
	if n := len(b.reconcile()); n != 0 {
		t.Fatalf("orphan state must produce 0 findings, got %d", n)
	}
}

// An identical re-issue (same orderId AND orderUpdateId) is idempotent — one
// finding, not a spurious LOST/DIVERGED. This encodes the verified VDA5050 rule.
func TestReconcile_IdempotentResend(t *testing.T) {
	b := newBuilder(t)
	b.issue("agv-01", "ORD", 1)
	b.issue("agv-01", "ORD", 1) // resend after a comms hiccup — must be a no-op
	b.issue("agv-01", "ORD", 1)
	b.report("agv-01", "ORD", 1, false)
	f := only(t, b.reconcile())
	if f.Status != reconcile.StatusAccepted {
		t.Fatalf("status = %s, want ACCEPTED (idempotent resend)", f.Status)
	}
}

func TestReconcile_Deterministic(t *testing.T) {
	b := newBuilder(t)
	b.issue("agv-02", "O2", 3)
	b.report("agv-02", "O2", 3, false)
	b.issue("agv-01", "O1", 2)
	b.report("agv-01", "O1", 1, false)
	entries := b.l.Entries()

	a, err := reconcile.Reconcile(entries)
	if err != nil {
		t.Fatal(err)
	}
	c, err := reconcile.Reconcile(entries)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, c) {
		t.Fatalf("Reconcile not deterministic:\n %+v\n %+v", a, c)
	}
	// Deterministic ordering: sorted by robot then order.
	if len(a) != 2 || a[0].RobotID != "agv-01" || a[1].RobotID != "agv-02" {
		t.Fatalf("findings not in stable order: %+v", a)
	}
}

// Assignment events (and any other kind) must be ignored, not produce findings.
func TestReconcile_IgnoresOtherKinds(t *testing.T) {
	b := newBuilder(t)
	b.append(event.KindAssign, event.Assign{TaskID: "t1", RobotID: "agv-01"})
	b.issue("agv-01", "ORD", 1)
	b.report("agv-01", "ORD", 1, false)
	if n := len(b.reconcile()); n != 1 {
		t.Fatalf("want 1 finding (assign ignored), got %d", n)
	}
}

// Findings must survive a real crash/recover cycle and the chain must stay intact.
func TestReconcile_SurvivesRecoverCycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")
	l, err := ledger.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustAppend := func(kind string, p any) {
		raw, _ := json.Marshal(p)
		if _, err := l.Append(ts(0), kind, raw); err != nil {
			t.Fatal(err)
		}
	}
	mustAppend(event.KindOrderIssued, event.OrderIssued{RobotID: "agv-01", OrderID: "ORD", OrderUpdateID: 2})
	mustAppend(event.KindStateReported, event.StateReported{RobotID: "agv-01", OrderID: "ORD", OrderUpdateID: 1, Driving: false})
	l.Close()

	l2, err := ledger.Open(path) // fresh process
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer l2.Close()
	if broken, err := l2.Verify(); broken != -1 || err != nil {
		t.Fatalf("chain broken after recover: %d %v", broken, err)
	}
	f, err := reconcile.Reconcile(l2.Entries())
	if err != nil {
		t.Fatal(err)
	}
	if only(t, f).Status != reconcile.StatusStalled {
		t.Fatalf("stalled finding did not survive recover cycle: %+v", f)
	}
}
