// Package reconcile implements command-state reconciliation: a pure function
// over the audit ledger that classifies, after the fact, whether every order
// the controller issued was confirmed-accepted by the robot it was sent to.
//
// This is the audit ledger's payoff — the "accountability" layer, the fleet
// analogue of financial settlement reconciliation: an OrderIssued is a sent
// instruction; a StateReported echoing the same (orderId, orderUpdateId) is the
// settlement confirmation.
//
// Honesty about what the ledger can prove: from issued vs reported orderUpdateId
// the fold proves CONFIRMATION status, not the robot's internal intent. A robot
// reported behind the issued update is "unconfirmed", not provably "rejected" —
// true rejection needs the robot's error/terminal signals (a later layer). The
// status names reflect that: ACCEPTED, PENDING, STALLED, UNOBSERVED.
//
// VDA5050 grounding (v2.x): (orderId, orderUpdateId) is the protocol idempotency
// key. Re-issuing an identical order is a harmless no-op. (Note: the spec's
// SAME_ORDER_UPDATE_ID WARNING is a *different* condition — same orderUpdateId
// with *different content* — and is intentionally NOT what STALLED means here.)
//
// Reconcile is a pure, deterministic fold: no clock, no I/O, no randomness.
// The same ledger always yields the same findings.
package reconcile

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/arti1117/fleet-master-controller/internal/event"
	"github.com/arti1117/fleet-master-controller/internal/ledger"
)

// Status classifies an issued order against the robot's latest reported state.
type Status int

const (
	// StatusAccepted: the robot's latest state reported orderUpdateId >= issued.
	StatusAccepted Status = iota
	// StatusPending: robot behind the issued update but still driving — likely applying it.
	StatusPending
	// StatusStalled: robot behind the issued update and not currently driving — the
	// issued update is unconfirmed as of the last state (may be a mid-order pause
	// or a genuinely stuck robot; the ledger alone cannot distinguish).
	StatusStalled
	// StatusUnobserved: the order was issued but no state references it in the
	// ledger — unconfirmed (could be lost in transit, or simply never reported).
	StatusUnobserved
)

func (s Status) String() string {
	switch s {
	case StatusAccepted:
		return "ACCEPTED"
	case StatusPending:
		return "PENDING"
	case StatusStalled:
		return "STALLED"
	case StatusUnobserved:
		return "UNOBSERVED"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// Finding is the reconciliation verdict for one issued (robot, order).
type Finding struct {
	RobotID          string
	OrderID          string
	IssuedUpdateID   uint32
	ObservedUpdateID uint32
	HasObservation   bool
	Status           Status
	Detail           string
}

type orderKey struct {
	robot string
	order string
}

// latestState is the robot's most recent reported state for an order. Update id
// and driving flag are kept together (from the SAME report) so the verdict, the
// numeric field, and the prose can never disagree.
type latestState struct {
	updateID uint32
	driving  bool
	seen     bool
}

// Reconcile folds the ledger into one Finding per issued (robot, order). It
// decodes only OrderIssued and StateReported entries and ignores all other
// kinds. A StateReported for an order that was never issued (an "orphan"
// unsolicited report) is intentionally OUT OF SCOPE for this unit — it is a
// different property (uncommanded activity) and produces no finding here.
// Findings are returned in a deterministic order (by robot, then order).
func Reconcile(entries []ledger.Entry) ([]Finding, error) {
	issuedMax := map[orderKey]uint32{}
	latest := map[orderKey]latestState{}

	for _, e := range entries {
		switch e.Kind {
		case event.KindOrderIssued:
			var o event.OrderIssued
			if err := json.Unmarshal(e.Payload, &o); err != nil {
				return nil, fmt.Errorf("reconcile: decode order_issued at index %d: %w", e.Index, err)
			}
			k := orderKey{o.RobotID, o.OrderID}
			// Highest issued update is the controller's current intent. An
			// identical re-issue leaves it unchanged — idempotent, never a
			// spurious finding.
			if o.OrderUpdateID > issuedMax[k] {
				issuedMax[k] = o.OrderUpdateID
			} else if _, ok := issuedMax[k]; !ok {
				issuedMax[k] = o.OrderUpdateID
			}

		case event.KindStateReported:
			var s event.StateReported
			if err := json.Unmarshal(e.Payload, &s); err != nil {
				return nil, fmt.Errorf("reconcile: decode state_reported at index %d: %w", e.Index, err)
			}
			// Entries are in ledger order; the last one wins. Keep updateID and
			// driving from the same (latest) report.
			latest[orderKey{s.RobotID, s.OrderID}] = latestState{updateID: s.OrderUpdateID, driving: s.Driving, seen: true}
		}
	}

	findings := make([]Finding, 0, len(issuedMax))
	for k, issued := range issuedMax {
		f := Finding{RobotID: k.robot, OrderID: k.order, IssuedUpdateID: issued}
		st, ok := latest[k]
		if ok && st.seen {
			f.HasObservation = true
			f.ObservedUpdateID = st.updateID
		}

		switch {
		case !f.HasObservation:
			f.Status = StatusUnobserved
			f.Detail = fmt.Sprintf("issued orderUpdateId %d but no state from robot %q references order %q — unconfirmed",
				issued, k.robot, k.order)
		case st.updateID >= issued:
			f.Status = StatusAccepted
			f.Detail = fmt.Sprintf("robot confirmed orderUpdateId %d (issued %d)", st.updateID, issued)
		case st.driving:
			f.Status = StatusPending
			f.Detail = fmt.Sprintf("robot at orderUpdateId %d, issued %d — still driving, may not have applied the update yet",
				st.updateID, issued)
		default:
			f.Status = StatusStalled
			f.Detail = fmt.Sprintf("robot at orderUpdateId %d, issued %d, not currently driving — issued update unconfirmed (mid-order pause or stuck)",
				st.updateID, issued)
		}
		findings = append(findings, f)
	}

	sort.Slice(findings, func(a, b int) bool {
		if findings[a].RobotID != findings[b].RobotID {
			return findings[a].RobotID < findings[b].RobotID
		}
		return findings[a].OrderID < findings[b].OrderID
	})
	return findings, nil
}

// CountByStatus tallies findings by status — convenient for CLI exit codes.
func CountByStatus(findings []Finding) map[Status]int {
	out := map[Status]int{}
	for _, f := range findings {
		out[f.Status]++
	}
	return out
}
