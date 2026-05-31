package ui

import (
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// ---- UI Element Styles (auto-detected dark/light) ----

var (
	isDark             bool
	LogoStyle          lipgloss.Style
	UserMsgStyle       lipgloss.Style
	AssistantMsgStyle  lipgloss.Style
	ToolTreeStyle      lipgloss.Style
	SpinnerStyle       lipgloss.Style
	SpinnerDoneStyle   lipgloss.Style
	StatusBarStyle     lipgloss.Style
	InputPromptStyle   lipgloss.Style
	InputBoxStyle      lipgloss.Style
	ErrorStyle         lipgloss.Style
	DimStyle           lipgloss.Style
	SystemMsgStyle     lipgloss.Style
	SuggestionBox      lipgloss.Style
	SuggestionItem     lipgloss.Style
	SuggestionDesc     lipgloss.Style
	SuggestionHotkey   lipgloss.Style
	SuggestionSelected lipgloss.Style
)

func init() {
	isDark = termenv.HasDarkBackground()
	initStyles()
}

func initStyles() {
	if isDark {
		LogoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("109")).Bold(true)
		UserMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
		AssistantMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
		ToolTreeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
		SpinnerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("178"))
		SpinnerDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("108"))
		StatusBarStyle = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("250"))
		InputPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("109"))
		InputBoxStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("243")).Padding(0, 1)
		ErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
		DimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		SystemMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Italic(true)
		SuggestionBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("109")).Padding(0, 1).BorderBackground(lipgloss.Color("234")).Background(lipgloss.Color("234"))
		SuggestionItem = lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
		SuggestionDesc = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
		SuggestionHotkey = lipgloss.NewStyle().Foreground(lipgloss.Color("109"))
		SuggestionSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("109"))
	} else {
		LogoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("25")).Bold(true)
		UserMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("27"))
		AssistantMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("236"))
		ToolTreeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("130"))
		SpinnerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("136"))
		SpinnerDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("28"))
		StatusBarStyle = lipgloss.NewStyle().Background(lipgloss.Color("254")).Foreground(lipgloss.Color("236"))
		InputPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("25"))
		InputBoxStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("249")).Padding(0, 1)
		ErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("160"))
		DimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
		SystemMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Italic(true)
		SuggestionBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("25")).Padding(0, 1).BorderBackground(lipgloss.Color("255")).Background(lipgloss.Color("255"))
		SuggestionItem = lipgloss.NewStyle().Foreground(lipgloss.Color("236"))
		SuggestionDesc = lipgloss.NewStyle().Foreground(lipgloss.Color("249"))
		SuggestionHotkey = lipgloss.NewStyle().Foreground(lipgloss.Color("25"))
		SuggestionSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(lipgloss.Color("25"))
	}
}

// ---- Custom Glamour Style ----
// We use glamour.WithAutoStyle() which picks DarkStyleConfig or LightStyleConfig
// automatically. This file only provides the custom style if users want to override.
// For now, the built-in dark/light configs from glamour are good enough.

func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }
func uintPtr(u uint) *uint       { return &u }

// CustomDarkStyle is a warm, high-contrast style for dark terminals.
// Used when glamour's built-in dark style isn't sufficient.
var CustomDarkStyle = ansi.StyleConfig{
	Document: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			BlockPrefix: "\n",
			BlockSuffix: "\n",
			Color:       stringPtr("255"),
		},
		Margin: uintPtr(2),
	},
	BlockQuote: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color:  stringPtr("250"),
			Italic: boolPtr(true),
		},
		Indent:      uintPtr(1),
		IndentToken: stringPtr("▎ "),
	},
	Paragraph: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{},
	},
	List: ansi.StyleList{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{},
		},
		LevelIndent: 4,
	},
	Heading: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			BlockSuffix: "\n",
			Color:       stringPtr("75"),
			Bold:        boolPtr(true),
		},
	},
	H1: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix:          " ",
			Suffix:          " ",
			Color:           stringPtr("228"),
			BackgroundColor: stringPtr("63"),
			Bold:            boolPtr(true),
		},
	},
	H2: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "## ",
			Color:  stringPtr("228"),
			Bold:   boolPtr(true),
		},
	},
	H3: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "### ",
			Color:  stringPtr("185"),
			Bold:   boolPtr(true),
		},
	},
	H4: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "#### ",
			Color:  stringPtr("150"),
		},
	},
	H5: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "##### ",
		},
	},
	H6: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "###### ",
			Color:  stringPtr("242"),
			Bold:   boolPtr(false),
		},
	},
	Strikethrough: ansi.StylePrimitive{
		CrossedOut: boolPtr(true),
	},
	Emph: ansi.StylePrimitive{
		Italic: boolPtr(true),
		Color:  stringPtr("255"),
	},
	Strong: ansi.StylePrimitive{
		Bold: boolPtr(true),
	},
	HorizontalRule: ansi.StylePrimitive{
		Color:  stringPtr("240"),
		Format: "\n──────────────\n",
	},
	Item: ansi.StylePrimitive{
		BlockPrefix: "• ",
	},
	Enumeration: ansi.StylePrimitive{
		BlockPrefix: ". ",
	},
	Task: ansi.StyleTask{
		StylePrimitive: ansi.StylePrimitive{},
		Ticked:         "[✓] ",
		Unticked:       "[ ] ",
	},
	Link: ansi.StylePrimitive{
		Color:     stringPtr("75"),
		Underline: boolPtr(true),
	},
	LinkText: ansi.StylePrimitive{
		Color: stringPtr("75"),
		Bold:  boolPtr(true),
	},
	Image: ansi.StylePrimitive{
		Color:     stringPtr("212"),
		Underline: boolPtr(true),
	},
	ImageText: ansi.StylePrimitive{
		Color:  stringPtr("243"),
		Format: "📷 {{.text}}",
	},
	Code: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix:          " ",
			Suffix:          " ",
			Color:           stringPtr("215"),
			BackgroundColor: stringPtr("237"),
		},
	},
	CodeBlock: ansi.StyleCodeBlock{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:           stringPtr("254"),
				BackgroundColor: stringPtr("236"),
			},
			Margin: uintPtr(2),
		},
		Chroma: &ansi.Chroma{
			Text: ansi.StylePrimitive{
				Color: stringPtr("#E4E4E4"),
			},
			Error: ansi.StylePrimitive{
				Color:           stringPtr("#F1F1F1"),
				BackgroundColor: stringPtr("#FF5555"),
			},
			Comment: ansi.StylePrimitive{
				Color: stringPtr("#787878"),
			},
			CommentPreproc: ansi.StylePrimitive{
				Color: stringPtr("#FF875F"),
			},
			Keyword: ansi.StylePrimitive{
				Color: stringPtr("#5F9FFF"),
			},
			KeywordReserved: ansi.StylePrimitive{
				Color: stringPtr("#FF87D7"),
			},
			KeywordNamespace: ansi.StylePrimitive{
				Color: stringPtr("#FF5F87"),
			},
			KeywordType: ansi.StylePrimitive{
				Color: stringPtr("#8787FF"),
			},
			Operator: ansi.StylePrimitive{
				Color: stringPtr("#FF8787"),
			},
			Punctuation: ansi.StylePrimitive{
				Color: stringPtr("#E8E8A8"),
			},
			Name: ansi.StylePrimitive{
				Color: stringPtr("#E4E4E4"),
			},
			NameBuiltin: ansi.StylePrimitive{
				Color: stringPtr("#FF87D7"),
			},
			NameTag: ansi.StylePrimitive{
				Color: stringPtr("#B083EA"),
			},
			NameAttribute: ansi.StylePrimitive{
				Color: stringPtr("#87AFFF"),
			},
			NameClass: ansi.StylePrimitive{
				Color:     stringPtr("#FFFFAF"),
				Underline: boolPtr(true),
				Bold:      boolPtr(true),
			},
			NameConstant: ansi.StylePrimitive{
				Color: stringPtr("#FF87D7"),
			},
			NameDecorator: ansi.StylePrimitive{
				Color: stringPtr("#FFFF87"),
			},
			NameFunction: ansi.StylePrimitive{
				Color: stringPtr("#5FFF87"),
			},
			LiteralNumber: ansi.StylePrimitive{
				Color: stringPtr("#87FFD7"),
			},
			LiteralString: ansi.StylePrimitive{
				Color: stringPtr("#D7AF87"),
			},
			LiteralStringEscape: ansi.StylePrimitive{
				Color: stringPtr("#AFFFD7"),
			},
			GenericDeleted: ansi.StylePrimitive{
				Color: stringPtr("#FF5F5F"),
			},
			GenericEmph: ansi.StylePrimitive{
				Italic: boolPtr(true),
			},
			GenericInserted: ansi.StylePrimitive{
				Color: stringPtr("#5FFF87"),
			},
			GenericStrong: ansi.StylePrimitive{
				Bold: boolPtr(true),
			},
			GenericSubheading: ansi.StylePrimitive{
				Color: stringPtr("#888888"),
			},
			Background: ansi.StylePrimitive{
				BackgroundColor: stringPtr("#303030"),
			},
		},
	},
	Table: ansi.StyleTable{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{},
		},
		CenterSeparator: stringPtr("┼"),
		ColumnSeparator: stringPtr("│"),
		RowSeparator:    stringPtr("─"),
	},
	DefinitionDescription: ansi.StylePrimitive{
		BlockPrefix: "\n  ",
	},
}
