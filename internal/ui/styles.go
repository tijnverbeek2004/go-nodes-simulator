package ui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	purple  = lipgloss.Color("99")
	green   = lipgloss.Color("78")
	red     = lipgloss.Color("196")
	yellow  = lipgloss.Color("214")
	dim     = lipgloss.Color("241")
	white   = lipgloss.Color("255")
	cyan    = lipgloss.Color("81")
	darkGray = lipgloss.Color("236")

	// Title bar
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(purple).
			MarginBottom(1)

	// Phase list
	checkMark  = lipgloss.NewStyle().Foreground(green).SetString("  ✓ ")
	crossMark  = lipgloss.NewStyle().Foreground(red).SetString("  ✗ ")
	spinnerDot = lipgloss.NewStyle().Foreground(purple).SetString("  ● ")
	pendingDot = lipgloss.NewStyle().Foreground(dim).SetString("  · ")

	phaseNameStyle = lipgloss.NewStyle().Width(24).Foreground(white)
	phaseInfoStyle = lipgloss.NewStyle().Foreground(dim)

	// Timeline table
	timelineHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(purple).
				MarginTop(1).
				MarginBottom(0)

	colTimeStyle   = lipgloss.NewStyle().Width(10).Foreground(dim)
	colActionStyle = lipgloss.NewStyle().Width(12).Foreground(cyan)
	colTargetStyle = lipgloss.NewStyle().Width(20).Foreground(white)
	colStatusDone  = lipgloss.NewStyle().Foreground(green)
	colStatusFail  = lipgloss.NewStyle().Foreground(red)
	colStatusRun   = lipgloss.NewStyle().Foreground(purple)
	colStatusWait  = lipgloss.NewStyle().Foreground(dim)

	headerRowStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(dim).
			BorderBottom(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(darkGray)

	// Final report
	reportTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(purple).
				MarginTop(1)

	reportBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(purple).
				Padding(1, 2)

	sectionTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(white).
			MarginTop(1).
			MarginBottom(0)

	// Table cell styles for report
	cellHeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(purple).
			PaddingRight(2)

	cellStyle = lipgloss.NewStyle().
			Foreground(white).
			PaddingRight(2)

	cellDimStyle = lipgloss.NewStyle().
			Foreground(dim).
			PaddingRight(2)

	stateRunning = lipgloss.NewStyle().Foreground(green)
	stateExited  = lipgloss.NewStyle().Foreground(red)
	stateOther   = lipgloss.NewStyle().Foreground(yellow)

	errorStyle = lipgloss.NewStyle().Foreground(red).Bold(true)
	savedStyle = lipgloss.NewStyle().Foreground(dim).MarginTop(1)
)
