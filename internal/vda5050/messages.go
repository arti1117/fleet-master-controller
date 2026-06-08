// Package vda5050 holds the subset of the VDA5050 v2.0 interface that the
// fleet controller exchanges with AGVs/AMRs over MQTT. Only the fields the
// demos exercise are modeled; the rest can be added as needed.
//
// Topic convention (VDA5050 §6.1):
//
//	{interfaceName}/{majorVersion}/{manufacturer}/{serialNumber}/{topic}
//	e.g. uagv/v2/KIT/agv-01/order
package vda5050

import "time"

// Header is common to every VDA5050 message.
type Header struct {
	HeaderID     uint32    `json:"headerId"`
	Timestamp    time.Time `json:"timestamp"`
	Version      string    `json:"version"` // "2.0.0"
	Manufacturer string    `json:"manufacturer"`
	SerialNumber string    `json:"serialNumber"` // robot identity
}

// Order is sent controller -> AGV: a sequence of nodes/edges to traverse.
type Order struct {
	Header
	OrderID       string `json:"orderId"`
	OrderUpdateID uint32 `json:"orderUpdateId"`
	Nodes         []Node `json:"nodes"`
	Edges         []Edge `json:"edges"`
}

type Node struct {
	NodeID       string        `json:"nodeId"`
	SequenceID   uint32        `json:"sequenceId"`
	Released     bool          `json:"released"`
	NodePosition *NodePosition `json:"nodePosition,omitempty"`
}

type NodePosition struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	MapID string  `json:"mapId"`
}

type Edge struct {
	EdgeID      string `json:"edgeId"`
	SequenceID  uint32 `json:"sequenceId"`
	Released    bool   `json:"released"`
	StartNodeID string `json:"startNodeId"`
	EndNodeID   string `json:"endNodeId"`
}

// State is sent AGV -> controller: the heartbeat the controller reasons over
// for recovery (②) and dropout reassignment (④).
type State struct {
	Header
	OrderID            string       `json:"orderId"`
	OrderUpdateID      uint32       `json:"orderUpdateId"`
	LastNodeID         string       `json:"lastNodeId"`
	LastNodeSequenceID uint32       `json:"lastNodeSequenceId"`
	Driving            bool         `json:"driving"`
	OperatingMode      string       `json:"operatingMode"` // AUTOMATIC, MANUAL, ...
	Errors             []ErrorEntry `json:"errors"`
	BatteryState       BatteryState `json:"batteryState"`
}

type ErrorEntry struct {
	ErrorType  string `json:"errorType"`
	ErrorLevel string `json:"errorLevel"` // WARNING | FATAL
}

type BatteryState struct {
	BatteryCharge float64 `json:"batteryCharge"` // percent
	Charging      bool    `json:"charging"`
}

// Connection is sent AGV -> controller (retained, with MQTT last-will) so the
// controller can detect a dropped robot for reassignment (④).
type Connection struct {
	Header
	ConnectionState string `json:"connectionState"` // ONLINE | OFFLINE | CONNECTIONBROKEN
}

const (
	ConnOnline           = "ONLINE"
	ConnOffline          = "OFFLINE"
	ConnConnectionBroken = "CONNECTIONBROKEN"
)
