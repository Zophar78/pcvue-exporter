package pcvue

import "time"

type VariableValue struct {
	Name      string
	Value     float64
	Quality   float64
	Timestamp time.Time
}

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
