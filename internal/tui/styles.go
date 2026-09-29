package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorPurple = lipgloss.Color("99")
	colorGreen  = lipgloss.Color("42")
	colorYellow = lipgloss.Color("214")
	colorRed    = lipgloss.Color("203")
	colorGray   = lipgloss.Color("240")
	colorWhite  = lipgloss.Color("255")
	colorOwl    = lipgloss.Color("#A78BFA")
	colorAmber  = lipgloss.Color("#FBBF24")
	colorCyan   = lipgloss.Color("#22D3EE")

	envProdStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#DC2626")).Padding(0, 1)
	envOtherStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0F172A")).Background(lipgloss.Color("#22D3EE")).Padding(0, 1)
	envUnkStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0F172A")).Background(lipgloss.Color("#FBBF24")).Padding(0, 1)

	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(colorPurple)

	sidebarBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorGray).
			Padding(0, 1)

	transcriptBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorGray).
				Padding(0, 1)

	userLineStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorWhite)
	textLineStyle    = lipgloss.NewStyle().Foreground(colorWhite)
	routeLineStyle   = lipgloss.NewStyle().Foreground(colorGray).Italic(true)
	errorLineStyle   = lipgloss.NewStyle().Foreground(colorRed).Bold(true)
	planTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorPurple)
	planDoneStyle    = lipgloss.NewStyle().Foreground(colorGreen)
	planActiveStyle  = lipgloss.NewStyle().Foreground(colorYellow)
	planPendingStyle = lipgloss.NewStyle().Foreground(colorGray)

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPurple).
			Padding(0, 1)

	modalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorYellow).
			Padding(1, 2).
			Bold(true)

	statusBarStyle = lipgloss.NewStyle().Foreground(colorGray)

	promptMarkStyle = lipgloss.NewStyle().Foreground(colorCyan).Bold(true)
	bulletStyle     = lipgloss.NewStyle().Foreground(colorOwl)
	toolNameStyle   = lipgloss.NewStyle().Foreground(colorWhite).Bold(true)
	sayStyle        = lipgloss.NewStyle().Foreground(colorGray).Italic(true)
)

func decisionStyle(decision string) lipgloss.Style {
	switch decision {
	case "ALLOW":
		return lipgloss.NewStyle().Foreground(colorGreen).Bold(true)
	case "BLOCK":
		return lipgloss.NewStyle().Foreground(colorRed).Bold(true)
	case "REVIEW", "ESCALATE":
		return lipgloss.NewStyle().Foreground(colorYellow).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(colorGray)
	}
}
