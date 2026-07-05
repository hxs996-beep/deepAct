package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// countHunkAddsDeletes counts added (+) and deleted (-) lines in a hunk body.
// Lines starting with "+++" / "---" (file headers) are not counted.
func countHunkAddsDeletes(hunk string) (adds, deletes int) {
	for _, line := range strings.Split(hunk, "\n") {
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '+':
			if !strings.HasPrefix(line, "+++") {
				adds++
			}
		case '-':
			if !strings.HasPrefix(line, "---") {
				deletes++
			}
		}
	}
	return adds, deletes
}

// hunkSummaryLine renders one collapsed hunk summary line:
//
//	[N] @@ -1,3 +1,3 @@    +2  -1
func hunkSummaryLine(idx int, hunkHeader string, adds, deletes int) string {
	numStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("210"))
	label := numStyle.Render(fmt.Sprintf("  [%d] ", idx+1))
	changes := addStyle.Render(fmt.Sprintf("+%d", adds)) + " " + delStyle.Render(fmt.Sprintf("-%d", deletes))
	return label + hunkHeader + "    " + changes
}
