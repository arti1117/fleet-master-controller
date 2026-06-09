// Package event defines the domain events recorded in the audit ledger. They
// are the vocabulary shared between the writer (fleet core) and the reader
// (recovery replay), kept in their own package so neither side imports the
// other.
package event

// Ledger entry kinds.
const (
	KindAssign        = "assign"         // a task was granted to a robot
	KindReassign      = "reassign"       // a task moved off a dropped robot to a healthy one
	KindOrderIssued   = "order_issued"   // controller issued a VDA5050 order (update) to a robot
	KindStateReported = "state_reported" // a robot reported its VDA5050 state
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

// OrderIssued records that the controller sent a VDA5050 order (or order update)
// to a robot. (orderId, orderUpdateId) is the VDA5050 idempotency key — the
// direct analogue of a payment idempotency key. Used by internal/reconcile to
// prove command-acceptance accountability.
type OrderIssued struct {
	RobotID       string `json:"robotId"` // VDA5050 serialNumber
	OrderID       string `json:"orderId"`
	OrderUpdateID uint32 `json:"orderUpdateId"`
	TaskID        string `json:"taskId,omitempty"` // links back to an Assign, if any
}

// StateReported records the reliability-relevant fields of a VDA5050 state
// message from a robot. Modeled against VDA5050 v2.x (this repo declares v2).
type StateReported struct {
	RobotID       string `json:"robotId"` // VDA5050 serialNumber
	OrderID       string `json:"orderId"`
	OrderUpdateID uint32 `json:"orderUpdateId"` // the order update the robot has accepted
	// Driving is true while the AGV is physically moving; per VDA5050 it goes
	// false at every node stop, action execution (load/unload), or wait — it is
	// NOT a terminal/done signal.
	Driving bool `json:"driving"`
}
