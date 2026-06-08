package vda5050

import "fmt"

// Standard VDA5050 topic leaves (§6.1).
const (
	TopicOrder          = "order"
	TopicInstantActions = "instantActions"
	TopicState          = "state"
	TopicConnection     = "connection"
	TopicVisualization  = "visualization"
	TopicFactsheet      = "factsheet"
)

// InterfaceName is the protocol interface prefix; "uagv" = unmanned ground vehicle.
const InterfaceName = "uagv"

// MajorVersion is the topic-level version segment for VDA5050 v2.x.
const MajorVersion = "v2"

// Topic builds a fully-qualified MQTT topic for a given robot and leaf:
//
//	uagv/v2/{manufacturer}/{serial}/{leaf}
func Topic(manufacturer, serial, leaf string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", InterfaceName, MajorVersion, manufacturer, serial, leaf)
}
