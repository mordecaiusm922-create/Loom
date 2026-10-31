package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Graphite + teal. Green, amber and red are reserved for verdicts.
	colorAccent     = lipgloss.Color("#2DD4BF")
	colorAccentSoft = lipgloss.Color("#5EEAD4")
	colorMuted      = lipgloss.Color("#94A3B8")
	colorBorder     = lipgloss.Color("#334155")
	colorWhite      = lipgloss.Color("#F1F5F9")
	colorGreen      = lipgloss.Color("#22C55E")
	colorYellow     = lipgloss.Color("#F59E0B")
	colorRed        = lipgloss.Color("#EF4444")

	envProdStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#DC2626")).Padding(0, 1)
	envOtherStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0A0E12")).Background(colorAccent).Padding(0, 1)
	envUnkStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0A0E12")).Background(colorYellow).Padding(0, 1)

	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	sidebarBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	transcriptBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorBorder).
				Padding(0, 1)

	userLineStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorWhite)
	textLineStyle    = lipgloss.NewStyle().Foreground(colorWhite)
	routeLineStyle   = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	errorLineStyle   = lipgloss.NewStyle().Foreground(colorRed).Bold(true)
	planTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	planDoneStyle    = lipgloss.NewStyle().Foreground(colorGreen)
	planActiveStyle  = lipgloss.NewStyle().Foreground(colorYellow)
	planPendingStyle = lipgloss.NewStyle().Foreground(colorMuted)

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(0, 1)

	modalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorYellow).
			Padding(1, 2).
			Bold(true)

	statusBarStyle = lipgloss.NewStyle().Foreground(colorMuted)

	promptMarkStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	bulletStyle     = lipgloss.NewStyle().Foreground(colorAccent)
	toolNameStyle   = lipgloss.NewStyle().Foreground(colorWhite).Bold(true)
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
		return lipgloss.NewStyle().Foreground(colorMuted)
	}
}
