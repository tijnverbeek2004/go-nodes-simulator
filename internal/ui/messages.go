package ui

import "github.com/tijnverbeek2004/nodetester/pkg/types"

// Messages sent from the scenario goroutine to the Bubble Tea model.

type phaseStartMsg struct {
	name string
	info string
}

type phaseUpdateMsg struct {
	name string
	info string
}

type phaseDoneMsg struct {
	name string
	info string
}

type phaseErrorMsg struct {
	name string
	err  error
}

type eventScheduledMsg struct {
	events []eventEntry
}

type eventEntry struct {
	at     string
	action string
	target string
}

type eventRunningMsg struct {
	index int
}

type eventDoneMsg struct {
	index   int
	success bool
	errMsg  string
}

type scenarioDoneMsg struct {
	nodes      []types.NodeStatus
	events     []types.EventRecord
	reportPath string
	err        error
}
