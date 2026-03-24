package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tijnverbeek2004/nodetester/pkg/types"
)

type phaseStatus int

const (
	phasePending phaseStatus = iota
	phaseActive
	phaseDone_
	phaseError
)

type phase struct {
	name   string
	info   string
	status phaseStatus
}

type eventDisplay struct {
	at     string
	action string
	target string
	status string // "pending", "running", "done", "failed"
	errMsg string
}

type model struct {
	ch      <-chan tea.Msg
	spinner spinner.Model

	// Scenario info
	scenarioPath string
	nodeCount    int
	imageName    string

	// Phase tracking
	phases []phase

	// Events timeline
	events []eventDisplay

	// Final report
	nodes      []types.NodeStatus
	records    []types.EventRecord
	reportPath string

	// State
	err      error
	done     bool
	quitting bool
	width    int
}

func newModel(scenarioPath string, scenario *types.Scenario, ch <-chan tea.Msg) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(purple)

	image := scenario.Nodes.Image
	if image == "" && scenario.Nodes.Preset == "ethereum" {
		image = "ethereum/client-go:v1.13.15"
	}

	return model{
		ch:           ch,
		spinner:      s,
		scenarioPath: scenarioPath,
		nodeCount:    scenario.Nodes.Count,
		imageName:    image,
		phases:       []phase{},
	}
}

func waitForMsg(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		waitForMsg(m.ch),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case phaseStartMsg:
		m.phases = append(m.phases, phase{
			name:   msg.name,
			info:   msg.info,
			status: phaseActive,
		})
		return m, waitForMsg(m.ch)

	case phaseUpdateMsg:
		for i := range m.phases {
			if m.phases[i].name == msg.name {
				m.phases[i].info = msg.info
				break
			}
		}
		return m, waitForMsg(m.ch)

	case phaseDoneMsg:
		for i := range m.phases {
			if m.phases[i].name == msg.name {
				m.phases[i].status = phaseDone_
				if msg.info != "" {
					m.phases[i].info = msg.info
				}
				break
			}
		}
		return m, waitForMsg(m.ch)

	case phaseErrorMsg:
		for i := range m.phases {
			if m.phases[i].name == msg.name {
				m.phases[i].status = phaseError
				m.phases[i].info = msg.err.Error()
				break
			}
		}
		m.err = msg.err
		return m, waitForMsg(m.ch)

	case eventScheduledMsg:
		m.events = make([]eventDisplay, len(msg.events))
		for i, e := range msg.events {
			m.events[i] = eventDisplay{
				at:     e.at,
				action: e.action,
				target: e.target,
				status: "pending",
			}
		}
		return m, waitForMsg(m.ch)

	case eventRunningMsg:
		if msg.index < len(m.events) {
			m.events[msg.index].status = "running"
		}
		return m, waitForMsg(m.ch)

	case eventDoneMsg:
		if msg.index < len(m.events) {
			if msg.success {
				m.events[msg.index].status = "done"
			} else {
				m.events[msg.index].status = "failed"
				m.events[msg.index].errMsg = msg.errMsg
			}
		}
		return m, waitForMsg(m.ch)

	case scenarioDoneMsg:
		m.done = true
		m.nodes = msg.nodes
		m.records = msg.events
		m.reportPath = msg.reportPath
		m.err = msg.err
		return m, tea.Quit

	case nil:
		return m, tea.Quit
	}

	return m, nil
}

func (m model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render("  nodetester"))
	b.WriteString("\n")
	divider := lipgloss.NewStyle().Foreground(darkGray).Render(strings.Repeat("─", 50))
	b.WriteString("  " + divider + "\n")

	// Phases
	for _, p := range m.phases {
		var icon string
		switch p.status {
		case phaseActive:
			icon = m.spinner.View() + " "
		case phaseDone_:
			icon = checkMark.String()
		case phaseError:
			icon = crossMark.String()
		default:
			icon = pendingDot.String()
		}

		name := phaseNameStyle.Render(p.name)
		info := phaseInfoStyle.Render(p.info)
		b.WriteString(icon + name + info + "\n")
	}

	// Events timeline
	if len(m.events) > 0 {
		b.WriteString("\n")
		b.WriteString(timelineHeaderStyle.Render("  Events"))
		b.WriteString("\n")

		// Header row
		hdr := fmt.Sprintf("  %s%s%s%s",
			lipgloss.NewStyle().Width(10).Bold(true).Foreground(dim).Render("TIME"),
			lipgloss.NewStyle().Width(12).Bold(true).Foreground(dim).Render("ACTION"),
			lipgloss.NewStyle().Width(20).Bold(true).Foreground(dim).Render("TARGET"),
			lipgloss.NewStyle().Bold(true).Foreground(dim).Render("STATUS"),
		)
		b.WriteString(hdr + "\n")
		b.WriteString("  " + lipgloss.NewStyle().Foreground(darkGray).Render(strings.Repeat("─", 48)) + "\n")

		for _, e := range m.events {
			time := colTimeStyle.Render(e.at)
			action := colActionStyle.Render(e.action)
			target := colTargetStyle.Render(e.target)

			var status string
			switch e.status {
			case "done":
				status = colStatusDone.Render("✓ done")
			case "failed":
				status = colStatusFail.Render("✗ fail")
			case "running":
				status = colStatusRun.Render(m.spinner.View() + " run")
			default:
				status = colStatusWait.Render("· wait")
			}

			b.WriteString("  " + time + action + target + status + "\n")
		}
	}

	// Final report
	if m.done && m.err == nil {
		b.WriteString(m.renderReport())
	}

	if m.done && m.err != nil {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render("  Error: " + m.err.Error()))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	return b.String()
}

func (m model) renderReport() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(reportTitleStyle.Render("  Results"))
	b.WriteString("\n")

	// Nodes table
	if len(m.nodes) > 0 {
		b.WriteString(sectionTitle.Render("  Nodes"))
		b.WriteString("\n")

		// Header
		b.WriteString(fmt.Sprintf("  %s%s%s%s\n",
			cellHeaderStyle.Render("NAME"),
			cellHeaderStyle.Copy().Width(16).Render("CONTAINER"),
			cellHeaderStyle.Copy().Width(12).Render("STATE"),
			cellHeaderStyle.Render("RESTARTS"),
		))
		b.WriteString("  " + lipgloss.NewStyle().Foreground(darkGray).Render(strings.Repeat("─", 52)) + "\n")

		for _, n := range m.nodes {
			name := cellStyle.Render(fmt.Sprintf("%-12s", n.Name))
			cid := cellDimStyle.Copy().Width(16).Render(n.ContainerID)

			var state string
			switch n.State {
			case "running":
				state = stateRunning.Copy().Width(12).Render(n.State)
			case "exited":
				state = stateExited.Copy().Width(12).Render(n.State)
			default:
				state = stateOther.Copy().Width(12).Render(n.State)
			}

			restarts := cellDimStyle.Render(fmt.Sprintf("%d", n.RestartCount))
			b.WriteString("  " + name + cid + state + restarts + "\n")
		}
	}

	// Events table
	if len(m.records) > 0 {
		b.WriteString(sectionTitle.Render("\n  Events"))
		b.WriteString("\n")

		b.WriteString(fmt.Sprintf("  %s%s%s%s\n",
			cellHeaderStyle.Copy().Width(12).Render("ACTION"),
			cellHeaderStyle.Copy().Width(20).Render("TARGET"),
			cellHeaderStyle.Copy().Width(10).Render("RESULT"),
			cellHeaderStyle.Render("ERROR"),
		))
		b.WriteString("  " + lipgloss.NewStyle().Foreground(darkGray).Render(strings.Repeat("─", 52)) + "\n")

		for _, e := range m.records {
			action := colActionStyle.Copy().Width(12).Render(e.Action)
			target := colTargetStyle.Copy().Width(20).Render(e.Target)

			var result string
			if e.Success {
				result = colStatusDone.Copy().Width(10).Render("✓")
			} else {
				result = colStatusFail.Copy().Width(10).Render("✗")
			}

			errMsg := ""
			if e.Error != "" {
				errMsg = lipgloss.NewStyle().Foreground(red).Render(truncate(e.Error, 30))
			}

			b.WriteString("  " + action + target + result + errMsg + "\n")
		}
	}

	if m.reportPath != "" {
		b.WriteString(savedStyle.Render(fmt.Sprintf("  Report saved to %s", m.reportPath)))
		b.WriteString("\n")
	}

	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
