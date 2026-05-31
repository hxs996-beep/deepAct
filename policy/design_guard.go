package policy

import (
	"strings"

	"github.com/deepact/deepact/engine"
)

type DesignReview = engine.DesignReview

type DesignIssue = engine.DesignIssue

const (
	VerdictPass    = "pass"
	VerdictWarning = "warning"
	VerdictBlock   = "blocking"

	SeverityBlocking = "blocking"
	SeverityWarning  = "warning"
)

type AntiPattern struct {
	Name        string
	Description string
	BadExample  string
	GoodExample string
}

var DefaultAntiPatterns = []AntiPattern{
	{
		Name:        "fragile-key",
		Description: "Using display text, content, or position as identifier when structured ID/name/type exists",
		BadExample:  `node.text == "Save Button" (breaks with i18n, text changes)`,
		GoodExample: `node.id == "btn-save" OR node.getAttribute("data-action") == "save"`,
	},
	{
		Name:        "position-dependent",
		Description: "Using array index/position as stable key when unique identifiers are available",
		BadExample:  `rows[3].cells[1] (breaks if table order changes)`,
		GoodExample: `table.findRow(id="user-123").getCell("email")`,
	},
	{
		Name:        "string-on-formatted",
		Description: "Regex/string matching on formatted output instead of using structured API",
		BadExample:  `output.contains("Error: 404") (fragile to message changes)`,
		GoodExample: `response.statusCode == 404`,
	},
	{
		Name:        "implementation-shortcut",
		Description: "Choosing shortest code path over correct abstraction; works only for current case",
		BadExample:  `if len(items) == 3 { return items[2] } (magic number, single-case)`,
		GoodExample: `findItem(items, criteria) (generic, robust)`,
	},
	{
		Name:        "hardcoded-derivable",
		Description: "Hardcoding values that should be derived from data structure or configuration",
		BadExample:  `path := "/usr/local/bin/tool" (platform-specific, not portable)`,
		GoodExample: `path := config.ToolPath() OR exec.LookPath("tool")`,
	},
}

type DesignGuard struct {
	Patterns []AntiPattern
}

func NewDesignGuard() *DesignGuard {
	return &DesignGuard{
		Patterns: DefaultAntiPatterns,
	}
}

func (g *DesignGuard) HasBlocking(review engine.DesignReview) bool {
	for _, issue := range review.Issues {
		if issue.Severity == SeverityBlocking {
			return true
		}
	}
	return false
}

func (g *DesignGuard) HasWarnings(review engine.DesignReview) bool {
	for _, issue := range review.Issues {
		if issue.Severity == SeverityWarning {
			return true
		}
	}
	return false
}

func (g *DesignGuard) BuildReviewPrompt(plan string, codeContext string) string {
	var builder strings.Builder
	builder.WriteString("Analyze this proposed approach for design anti-patterns.\n\n")
	builder.WriteString("## Proposed Approach:\n")
	builder.WriteString(plan)
	builder.WriteString("\n\n## Code Context:\n")
	builder.WriteString(codeContext)
	builder.WriteString("\n\n## Anti-Patterns to Check:\n")

	for _, p := range g.Patterns {
		builder.WriteString("- **")
		builder.WriteString(p.Name)
		builder.WriteString("**: ")
		builder.WriteString(p.Description)
		builder.WriteString("\n  BAD: ")
		builder.WriteString(p.BadExample)
		builder.WriteString("\n  GOOD: ")
		builder.WriteString(p.GoodExample)
		builder.WriteString("\n\n")
	}

	builder.WriteString(`For each issue found, respond with JSON:
{"verdict": "blocking"|"warning"|"pass", "issues": [{"pattern": "...", "severity": "...", "what": "...", "why": "...", "alternative": "..."}]}

If no issues: {"verdict": "pass", "issues": []}`)

	return builder.String()
}
