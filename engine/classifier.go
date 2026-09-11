package engine

import (
	"regexp"
	"strings"
)

var rememberRe = regexp.MustCompile(`<!--\s*REMEMBER:\s*(.+?)\s*-->`)

// extractRememberMarkers scans content for <!-- REMEMBER: ... --> markers.
// These are explicit memory annotations the model can use to persist important
// information across context compression.
func extractRememberMarkers(content string) []string {
	matches := rememberRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var markers []string
	for _, m := range matches {
		text := strings.TrimSpace(m[1])
		if text != "" && !seen[text] {
			seen[text] = true
			markers = append(markers, text)
		}
	}
	return markers
}
