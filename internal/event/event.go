// Package event defines the domain events recorded in the audit ledger. They
// are the vocabulary shared between the writer (fleet core) and the reader
// (recovery replay), kept in their own package so neither side imports the
// other.
package event

// Ledger entry kinds.
const (
	KindAssign   = "assign"   // a task was granted to a robot
	KindReassign = "reassign" // a task moved off a dropped robot to a healthy one
)

// Assign records that TaskID was granted to RobotID (demo ①/②).
type Assign struct {
	TaskID  string `json:"taskId"`
	RobotID string `json:"robotId"`
}

// Reassign records that TaskID moved from FromRobot to ToRobot (demo ④).
type Reassign struct {
	TaskID    string `json:"taskId"`
	FromRobot string `json:"fromRobot"`
	ToRobot   string `json:"toRobot"`
}
