package tui

import "github.com/charmbracelet/lipgloss"

// ad builds an adaptive color that resolves to the Light value on light
// terminals and the Dark value on dark terminals. The terminal background is
// detected once at startup (see main) so this never probes at render time.
func ad(light, dark string) lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Light: light, Dark: dark}
}

// Semantic palette. Each color adapts to the terminal background: the Light
// variant is darker (for contrast on a light background) and the Dark variant
// is lighter. Values are 256-color indices.
var (
	colorPrimary = ad("25", "39")   // blue   — accents, keys, borders
	colorAccent  = ad("162", "213") // pink   — assistant
	colorUser    = ad("28", "42")   // green  — user
	colorTool    = ad("166", "214") // orange — tools
	colorDim     = ad("240", "245") // secondary text
	colorMuted   = ad("245", "240") // tertiary / faint text
	colorRule    = ad("252", "237") // horizontal rules
	colorErr     = ad("160", "203") // errors
	colorBgBar   = ad("254", "236") // status bar background
	colorFg      = ad("235", "252") // body text on the terminal background
	colorOnBar   = ad("236", "231") // text on the status bar background

	// white is used for text drawn on a colored (blue) background, where it
	// reads well regardless of terminal theme.
	white = lipgloss.Color("231")

	// Diff colors.
	diffAdd = ad("28", "78")   // green additions
	diffDel = ad("160", "203") // red deletions
)

var (
	// Header.
	headerBadgeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(white).
				Background(colorPrimary).
				Padding(0, 1)

	headerMetaStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	headerKeyStyle = lipgloss.NewStyle().
			Foreground(colorFg).
			Bold(true)

	headerSepStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	// headerProjectStyle renders the merged project-dir / git-branch / dirty
	// token in the header. It's bold so the project name, branch, and status
	// stand out from the dim metadata around them.
	headerProjectStyle = lipgloss.NewStyle().
				Foreground(colorFg).
				Bold(true)

	ruleStyle = lipgloss.NewStyle().
			Foreground(colorRule)

	// Message gutters/labels. The vertical bar and label share a color so each
	// speaker reads as a distinct column down the transcript.
	userLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorUser)

	assistantLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent)

	toolLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorTool)

	toolMetaStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	toolOutputStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	bodyStyle = lipgloss.NewStyle().
			Foreground(colorFg)

	errorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorErr)

	infoStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Italic(true)

	// Extended thinking (reasoning) transcript blocks.
	thinkingLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorMuted)

	thinkingTextStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Italic(true)

	// Status bar.
	statusBarStyle = lipgloss.NewStyle().
			Foreground(colorOnBar).
			Background(colorBgBar).
			Padding(0, 1)

	statusKeyStyle = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Background(colorBgBar).
			Bold(true)

	statusBusyStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Background(colorBgBar).
			Bold(true)

	statusDimStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Background(colorBgBar)

	statusTextStyle = lipgloss.NewStyle().
			Foreground(colorOnBar).
			Background(colorBgBar)

	// Relay status pill on the status bar: green when connected, orange while
	// connecting or retrying.
	statusRelayOnStyle = lipgloss.NewStyle().
				Foreground(colorUser).
				Background(colorBgBar)

	statusRelayWaitStyle = lipgloss.NewStyle().
				Foreground(colorTool).
				Background(colorBgBar)

	// Context-fill indicator coloring. Normal uses the bar's regular text
	// color; warn flips to the tool-orange when the user is at 75-89% full;
	// alarm goes red at 90%+ to nudge a /compact or /reset.
	statusCtxNormStyle = lipgloss.NewStyle().
				Foreground(colorOnBar).
				Background(colorBgBar)

	statusCtxWarnStyle = lipgloss.NewStyle().
				Foreground(colorTool).
				Background(colorBgBar).
				Bold(true)

	statusCtxAlarmStyle = lipgloss.NewStyle().
				Foreground(colorErr).
				Background(colorBgBar).
				Bold(true)

	// Input.
	inputPromptStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Bold(true)

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorMuted).
			Padding(0, 1)

	inputBoxFocusStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorPrimary).
				Padding(0, 1)

	// Command hint menu.
	hintNameStyle = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Bold(true)

	hintArgStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	hintDescStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	// Welcome banner.
	welcomeBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPrimary).
			Padding(0, 2)

	welcomeKeyStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	// logoGradientDark/Light color the ASCII logo from top (blue) to bottom
	// (pink); the light ramp uses deeper tones for contrast on a light terminal.
	logoGradientDark = []lipgloss.Color{
		lipgloss.Color("39"),
		lipgloss.Color("75"),
		lipgloss.Color("111"),
		lipgloss.Color("177"),
		lipgloss.Color("213"),
	}
	logoGradientLight = []lipgloss.Color{
		lipgloss.Color("25"),
		lipgloss.Color("62"),
		lipgloss.Color("98"),
		lipgloss.Color("134"),
		lipgloss.Color("162"),
	}

	// Diff rendering.
	diffAddStyle     = lipgloss.NewStyle().Foreground(diffAdd)
	diffDelStyle     = lipgloss.NewStyle().Foreground(diffDel)
	diffContextStyle = lipgloss.NewStyle().Foreground(colorDim)
	diffMetaStyle    = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)

	// Approval prompt.
	approvalBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorTool).
				Padding(0, 1)

	approvalTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorTool)

	approvalKeyStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorPrimary)

	// Selection menu (e.g. /models, /reasoning).
	menuBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPrimary).
			Padding(0, 1)

	menuTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary)

	menuItemStyle = lipgloss.NewStyle().
			Foreground(colorFg)

	menuSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(white).
				Background(colorPrimary)

	menuDescStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	menuCurrentStyle = lipgloss.NewStyle().
				Foreground(colorUser)

	// Todo panel.
	todoBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorMuted).
			Padding(0, 1)

	todoTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary)

	todoPendingStyle    = lipgloss.NewStyle().Foreground(colorDim)
	todoInProgressStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	todoCompletedStyle  = lipgloss.NewStyle().Foreground(colorUser).Strikethrough(true)
	todoCancelledStyle  = lipgloss.NewStyle().Foreground(colorMuted).Strikethrough(true)

	// Left sidebar panel (toggled with ctrl+b). It hosts the Todos and MCP
	// sections to the left of the transcript.
	sidebarBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), false, true, false, false).
			BorderForeground(colorMuted).
			Padding(0, 1)

	sidebarSectionStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorPrimary)

	sidebarDimStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	sidebarMutedStyle = lipgloss.NewStyle().
				Foreground(colorMuted)

	// MCP server line: the name reads in the body color, the count is dimmed.
	mcpServerStyle = lipgloss.NewStyle().
			Foreground(colorFg)

	mcpCountStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	// Modified-files section: created files read green (like a diff addition),
	// edited files read in the tool-orange.
	fileCreatedStyle  = lipgloss.NewStyle().Foreground(diffAdd)
	fileModifiedStyle = lipgloss.NewStyle().Foreground(colorTool)

	// Startup loader (the animated "Checking auth / MCP / LSP" block
	// shown below the input while the background startup sequence runs).
	startupBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPrimary).
			Padding(0, 1)

	// A check that's running or finished shows its label in the body color; a
	// pending (not-yet-started) check is dimmed so the eye tracks the active row.
	startupLabelStyle   = lipgloss.NewStyle().Foreground(colorFg)
	startupPendingStyle = lipgloss.NewStyle().Foreground(colorMuted)
	startupDetailStyle  = lipgloss.NewStyle().Foreground(colorDim)
	startupOKStyle      = lipgloss.NewStyle().Bold(true).Foreground(colorUser)
	startupWarnStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorErr)
)

// logoGradientColors returns the logo color ramp for the current terminal
// background (detected once at startup).
func logoGradientColors() []lipgloss.Color {
	if lipgloss.HasDarkBackground() {
		return logoGradientDark
	}
	return logoGradientLight
}
