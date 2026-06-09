// Package recovery implements demo ② — exactly-once crash recovery. After the
// controller process dies and restarts, every in-flight task is recovered with
// no loss and no duplication: folding the durable audit ledger (③) back into
// state reproduces the exact assignment set that existed before the crash.
//
// Reliability property: Replay is deterministic and idempotent — replaying the
// same log twice yields identical state, and because every accepted assignment
// is durably in the ledger before it is acknowledged, a crash at any point
// never creates a duplicate or orphan task.
package recovery

import (
	"encoding/json"
	"fmt"

	"github.com/arti1117/fleet-master-controller/internal/event"
	"github.com/arti1117/fleet-master-controller/internal/ledger"
)

// State is the controller's recoverable view, rebuilt purely from the ledger.
type State struct {
	Assignments map[string]string // taskID -> robotID
}

// Replay reconstructs controller state from the ordered audit chain. Each entry
// is applied deterministically; applying the same entry twice is idempotent
// (assignment is a map write keyed by taskID), which is what makes recovery
// exactly-once rather than at-least-once.
func Replay(entries []ledger.Entry) (State, error) {
	st := State{Assignments: make(map[string]string)}
	for _, e := range entries {
		switch e.Kind {
		case event.KindAssign:
			var a event.Assign
			if err := json.Unmarshal(e.Payload, &a); err != nil {
				return State{}, fmt.Errorf("recovery: decode assign at index %d: %w", e.Index, err)
			}
			st.Assignments[a.TaskID] = a.RobotID
		case event.KindReassign:
			var r event.Reassign
			if err := json.Unmarshal(e.Payload, &r); err != nil {
				return State{}, fmt.Errorf("recovery: decode reassign at index %d: %w", e.Index, err)
			}
			st.Assignments[r.TaskID] = r.ToRobot
		case event.KindOrderIssued, event.KindStateReported:
			// Not part of the assignment projection — these are consumed by
			// internal/reconcile (command-state accountability). Accepted here
			// so they do not trip the fail-closed default during recovery.
		default:
			// Fail closed: an unknown kind means either a newer writer wrote a
			// record this version cannot interpret, or the log is corrupt.
			// Silently skipping it would desynchronize recovered state from the
			// true history.
			return State{}, fmt.Errorf("recovery: unknown event kind %q at index %d", e.Kind, e.Index)
		}
	}
	return st, nil
}
