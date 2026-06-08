// Package reassign implements demo ④ — graceful reassignment on robot dropout.
// When a robot goes OFFLINE/CONNECTIONBROKEN (detected via the VDA5050
// connection topic + MQTT last-will), its uncompleted tasks are atomically
// re-distributed to healthy robots with no task lost and none double-run.
//
// Reliability property to prove: a dropout during active task execution results
// in every orphaned task being reclaimed exactly once by a surviving robot, and
// the transition is recorded in the audit ledger (③) for after-the-fact proof.
package reassign

// Event is emitted when the controller decides to move a task off a dead robot.
type Event struct {
	TaskID    string
	FromRobot string
	ToRobot   string
	Reason    string // "connection_broken" | "fatal_error" | ...
}

// Reassigner watches robot health and redistributes work.
//
// TODO(demo④): subscribe to connection-state changes, find orphaned tasks via
// the allocator, re-Claim them onto healthy robots, and Append a "reassign"
// entry to the ledger; test by killing a robot mid-order and asserting the
// task set is conserved.
type Reassigner interface {
	// OnDropout handles a robot going offline and returns the reassignments made.
	OnDropout(robotID string) ([]Event, error)
}
