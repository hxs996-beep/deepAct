package tools

import (
	"fmt"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// contextLines is the number of unchanged lines shown around each change block.
const contextLines = 5

// GenerateUnifiedDiff generates a unified diff string between old and new file content.
// Returns a string in standard unified diff format with 5 lines of context.
func GenerateUnifiedDiff(oldContent, newContent, filePath string) string {
	if oldContent == newContent {
		return ""
	}

	oldLines := splitContentLines(oldContent)
	newLines := splitContentLines(newContent)

	dmp := diffmatchpatch.New()
	runes1, runes2, lineArray := linesToRunes(oldLines, newLines)
	diffs := dmp.DiffMainRunes(runes1, runes2, true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var buf strings.Builder
	fmt.Fprintf(&buf, "--- a/%s\n", filePath)
	fmt.Fprintf(&buf, "+++ b/%s\n", filePath)
	buf.WriteString(buildUnifiedDiffFromRunes(diffs, lineArray, contextLines))
	return buf.String()
}

// splitContentLines splits text into lines, discarding content after the last \n.
func splitContentLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	// If the text ends with \n, the last element is an empty string
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// linesToRunes encodes each unique line as a single rune, returning rune arrays
// and the line lookup table. This avoids the multi-digit index problem of DiffLinesToChars.
func linesToRunes(lines1, lines2 []string) ([]rune, []rune, []string) {
	lineArray := []string{""} // index 0 = sentinel (unused)
	lineHash := map[string]int{}

	encode := func(line string) rune {
		if idx, ok := lineHash[line]; ok {
			return rune(idx)
		}
		idx := len(lineArray)
		lineArray = append(lineArray, line)
		lineHash[line] = idx
		return rune(idx)
	}

	runes1 := make([]rune, len(lines1))
	for i, l := range lines1 {
		runes1[i] = encode(l)
	}

	runes2 := make([]rune, len(lines2))
	for i, l := range lines2 {
		runes2[i] = encode(l)
	}

	return runes1, runes2, lineArray
}

// lineEntry represents a single line in a diff, with its operation type and content.
type lineEntry struct {
	op   diffmatchpatch.Operation
	text string
}

// buildUnifiedDiffFromRunes builds a unified diff string from rune-encoded diffs.
func buildUnifiedDiffFromRunes(diffs []diffmatchpatch.Diff, lineArray []string, contextLines int) string {
	// Expand rune diffs into individual line entries
	var entries []lineEntry
	for _, d := range diffs {
		for _, r := range d.Text {
			idx := int(r)
			if idx <= 0 || idx >= len(lineArray) {
				continue
			}
			entries = append(entries, lineEntry{op: d.Type, text: lineArray[idx]})
		}
	}

	if len(entries) == 0 {
		return ""
	}

	// Post-process: align adjacent Delete+Insert blocks.
	// The diff algorithm may merge unchanged lines into a change block when they
	// sit between changes. Example: [foo, bar, baz] → [foo_modified, bar, baz_modified]
	// becomes Delete[foo, bar, baz] + Insert[foo_modified, bar, baz_modified].
	// We detect matching lines in Delete and Insert and split them into Equal.
	entries = alignDeleteInsertPairs(entries)

	// Precompute old/new line numbers for each entry
	type posEntry struct {
		oldNum int
		newNum int
		entry  lineEntry
	}
	positions := make([]posEntry, len(entries))
	oldNum, newNum := 1, 1
	for i, e := range entries {
		positions[i] = posEntry{oldNum, newNum, e}
		switch e.op {
		case diffmatchpatch.DiffEqual:
			oldNum++
			newNum++
		case diffmatchpatch.DiffDelete:
			oldNum++
		case diffmatchpatch.DiffInsert:
			newNum++
		}
	}

	// Find change regions (consecutive non-Equal entries)
	type region struct{ start, end int }
	var regions []region
	i := 0
	for i < len(positions) {
		if positions[i].entry.op != diffmatchpatch.DiffEqual {
			start := i
			for i < len(positions) && positions[i].entry.op != diffmatchpatch.DiffEqual {
				i++
			}
			regions = append(regions, region{start, i})
		} else {
			i++
		}
	}

	if len(regions) == 0 {
		return ""
	}

	// Merge close regions (git diff behavior: merge if ≤ 2*contextLines apart)
	var hunks []region
	currentStart := max(0, regions[0].start-contextLines)
	currentEnd := min(len(positions), regions[0].end+contextLines)

	for _, r := range regions[1:] {
		expandedStart := max(0, r.start-contextLines)
		if currentEnd >= expandedStart {
			currentEnd = min(len(positions), r.end+contextLines)
		} else {
			hunks = append(hunks, region{currentStart, currentEnd})
			currentStart = expandedStart
			currentEnd = min(len(positions), r.end+contextLines)
		}
	}
	hunks = append(hunks, region{currentStart, currentEnd})

	// Build unified diff output
	var buf strings.Builder
	for _, h := range hunks {
		oldStart := positions[h.start].oldNum
		newStart := positions[h.start].newNum
		oldCount := 0
		newCount := 0
		for j := h.start; j < h.end; j++ {
			switch positions[j].entry.op {
			case diffmatchpatch.DiffEqual:
				oldCount++
				newCount++
			case diffmatchpatch.DiffDelete:
				oldCount++
			case diffmatchpatch.DiffInsert:
				newCount++
			}
		}

		buf.WriteString(computeHunkHeader(oldStart, oldCount, newStart, newCount))

		for j := h.start; j < h.end; j++ {
			p := positions[j]
			switch p.entry.op {
			case diffmatchpatch.DiffEqual:
				buf.WriteString(" ")
				buf.WriteString(p.entry.text)
			case diffmatchpatch.DiffDelete:
				buf.WriteString("-")
				buf.WriteString(p.entry.text)
			case diffmatchpatch.DiffInsert:
				buf.WriteString("+")
				buf.WriteString(p.entry.text)
			}
			buf.WriteString("\n")
		}
	}

	return buf.String()
}

// alignDeleteInsertPairs walks adjacent Delete+Insert blocks and converts matching
// lines to Equal. This fixes cases where the LCS diff groups unchanged lines into
// a change block because they sit between two changes.
func alignDeleteInsertPairs(entries []lineEntry) []lineEntry {
	i := 0
	for i < len(entries) {
		if entries[i].op != diffmatchpatch.DiffDelete {
			i++
			continue
		}
		// Collect all Delete entries
		delStart := i
		for i < len(entries) && entries[i].op == diffmatchpatch.DiffDelete {
			i++
		}
		dels := entries[delStart:i]

		// Must be followed by Insert entries
		if i >= len(entries) || entries[i].op != diffmatchpatch.DiffInsert {
			continue
		}
		insStart := i
		for i < len(entries) && entries[i].op == diffmatchpatch.DiffInsert {
			i++
		}
		ins := entries[insStart:i]

		// Build a set of texts that appear in both dels and ins.
		inDels := make(map[string]int, len(dels))
		for _, d := range dels {
			inDels[d.text]++
		}

		// Walk both sequences simultaneously. For each position:
		// - If texts match, output Equal and advance both
		// - If only the delete text appears in both (so it exists in ins too),
		//   it's likely context. Emit the insert(s) before it first.
		// - Otherwise, emit Delete/Insert respectively.
		type alignedEntry struct {
			op   diffmatchpatch.Operation
			text string
		}
		var aligned []alignedEntry
		di, ii := 0, 0
		for di < len(dels) && ii < len(ins) {
			if dels[di].text == ins[ii].text {
				// Same line in both → context
				aligned = append(aligned, alignedEntry{op: diffmatchpatch.DiffEqual, text: dels[di].text})
				di++
				ii++
				continue
			}
			// Check if delete text appears somewhere ahead in ins
			foundDelInIns := false
			for k := ii; k < len(ins); k++ {
				if ins[k].text == dels[di].text {
					foundDelInIns = true
					break
				}
			}
			// Check if insert text appears somewhere ahead in dels
			foundInsInDels := false
			for k := di; k < len(dels); k++ {
				if dels[k].text == ins[ii].text {
					foundInsInDels = true
					break
				}
			}
			if foundDelInIns && !foundInsInDels {
				// Delete text matches an insert ahead → emit current insert(s)
				aligned = append(aligned, alignedEntry{op: diffmatchpatch.DiffInsert, text: ins[ii].text})
				ii++
			} else if foundInsInDels && !foundDelInIns {
				// Insert text matches a delete ahead → emit current delete
				aligned = append(aligned, alignedEntry{op: diffmatchpatch.DiffDelete, text: dels[di].text})
				di++
			} else {
				// No match ahead → emit both as actual change
				aligned = append(aligned, alignedEntry{op: diffmatchpatch.DiffDelete, text: dels[di].text})
				aligned = append(aligned, alignedEntry{op: diffmatchpatch.DiffInsert, text: ins[ii].text})
				di++
				ii++
			}
		}
		// Emit remaining deletes
		for di < len(dels) {
			aligned = append(aligned, alignedEntry{op: diffmatchpatch.DiffDelete, text: dels[di].text})
			di++
		}
		// Emit remaining inserts
		for ii < len(ins) {
			aligned = append(aligned, alignedEntry{op: diffmatchpatch.DiffInsert, text: ins[ii].text})
			ii++
		}

		// Rebuild the entry slice
		var result []lineEntry
		result = append(result, entries[:delStart]...)
		for _, a := range aligned {
			result = append(result, lineEntry{op: a.op, text: a.text})
		}
		result = append(result, entries[i:]...)
		entries = result
		i = delStart + len(aligned)
	}
	return entries
}

// computeHunkHeader creates the @@ -oldStart,oldCount +newStart,newCount @@ line.
func computeHunkHeader(oldStart, oldCount, newStart, newCount int) string {
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
}

// IsDiffContent checks if a string contains unified diff content.
func IsDiffContent(s string) bool {
	return strings.Contains(s, "\n--- a/") && strings.Contains(s, "\n+++ b/")
}

// SplitDiff extracts diff content from a multi-line digest.
// Returns (summaryLine, diffContent, hasDiff).
func SplitDiff(digest string) (summary string, diff string, hasDiff bool) {
	lines := strings.SplitN(digest, "\n", 2)
	if len(lines) == 0 {
		return "", "", false
	}
	summary = lines[0]
	if len(lines) < 2 {
		return summary, "", false
	}
	diff = lines[1]
	// Verify it looks like a diff
	if !strings.HasPrefix(strings.TrimSpace(diff), "--- a/") &&
		!strings.HasPrefix(strings.TrimSpace(diff), "@@") {
		return summary, "", false
	}
	return summary, diff, true
}
