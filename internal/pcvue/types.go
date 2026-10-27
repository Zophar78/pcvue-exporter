// Package pcvue provides a client and data types for the PCVue REST API.
package pcvue

import "time"

// VariableValue contains a PCVue variable's current value.
type VariableValue struct {
	Name  string
	Value float64
}

// Alarm contains the state and properties returned for a PCVue alarm event.
type Alarm struct {
	Name         string
	ID           string
	Severity     string
	AssocLabel   string
	Description  string
	EventCode    string
	EventValue   int
	Active       bool
	Acknowledged bool
	Timestamp    time.Time
}
