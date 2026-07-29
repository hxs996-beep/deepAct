package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderHunkLines renders a hunk's diff lines with simple foreground coloring:
// + lines green, - lines red, context/header dim. No background block styling,
// so each line truncates cleanly without misalignment (like git diff in a terminal).
// File headers (--- a/, +++ b/) are already stripped by parseDiffHunks, so any
// line starting with + or - here is a genuine diff line.
func renderHunkLines(hunk string) []string {
	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("210"))
	var lines []string
	for _, line := range strings.Split(hunk, "\n") {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			lines = append(lines, addStyle.Render("  "+line))
		case strings.HasPrefix(line, "-"):
			lines = append(lines, delStyle.Render("  "+line))
		default:
			lines = append(lines, DimStyle.Render("  "+line))
		}
	}
	return lines
}
