package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// parseDSMLToolCalls detects and extracts DSML-format tool calls from content text.
// DeepSeek models sometimes emit tool calls as raw DSML tokens in the content field
// instead of using the structured tool_calls API field. This function serves as a
// fallback parser to recover those tool calls.
//
// Returns:
//   - cleaned: content with DSML block removed
//   - calls: parsed tool calls (empty if none found)
//   - found: whether DSML tool calls were detected
func parseDSMLToolCalls(content string) (cleaned string, calls []ModelToolCall, found bool) {
	loc := dsmlBlockRe.FindStringIndex(content)
	if loc == nil {
		return content, nil, false
	}

	block := content[loc[0]:loc[1]]
	cleaned = strings.TrimSpace(content[:loc[0]] + content[loc[1]:])

	invokes := dsmlInvokeRe.FindAllStringSubmatch(block, -1)
	if len(invokes) == 0 {
		return cleaned, nil, true
	}

	for i, invoke := range invokes {
		if len(invoke) < 3 {
			continue
		}
		toolName := invoke[1]
		body := invoke[2]

		params := dsmlParamRe.FindAllStringSubmatch(body, -1)
		args := make(map[string]interface{})
		for _, param := range params {
			if len(param) < 3 {
				continue
			}
			paramName := param[1]
			paramValue := normalizeDSMLValue(param[2])
			args[paramName] = paramValue
		}

		argsJSON, err := json.Marshal(args)
		if err != nil {
			continue
		}

		call := ModelToolCall{
			ID:   fmt.Sprintf("dsml_call_%d", i),
			Type: "function",
			Function: ModelFunctionCall{
				Name:      toolName,
				Arguments: string(argsJSON),
			},
		}
		calls = append(calls, call)
	}

	return cleaned, calls, true
}

// stripDSMLTokens unconditionally removes ALL DSML markup from text.
// This is the final safety net — DSML tokens must NEVER be visible to the user.
func stripDSMLTokens(content string) string {
	if content == "" {
		return content
	}
	result := dsmlBlockRe.ReplaceAllString(content, "")
	result = dsmlIncompleteBlockRe.ReplaceAllString(result, "")
	result = dsmlAsciiBlockRe.ReplaceAllString(result, "")
	result = dsmlAsciiIncompleteRe.ReplaceAllString(result, "")
	return strings.TrimSpace(result)
}

func hasDSMLToolCalls(content string) bool {
	return dsmlDetectRe.MatchString(content)
}

// internalEchoHeaders returns the lines that identify an echoed internal block,
// derived from the blocks DeepAct actually injected into this turn's model
// input. Deriving instead of hand-listing keeps the stripper in sync with what
// is sent: the previous fixed list had accumulated dead entries (headers no
// longer produced, which silently deleted legitimate answer sections) and
// missed live blocks (codebase tree, AGENTS.md, skills, system-prompt sections).
//
// A line qualifies when it is structural: a markdown heading ("# ...") or a
// standalone bracketed marker ("[SESSION ARCHIVE]", "[SKILL — x]"). Qualifying
// lines are matched by whole-line equality, so a sentence that merely contains
// such a phrase is never stripped.
func internalEchoHeaders(injectedBlocks []string) map[string]bool {
	headers := make(map[string]bool)
	for _, block := range injectedBlocks {
		for _, line := range strings.Split(block, "\n") {
			trimmed := strings.TrimSpace(line)
			if isMarkdownHeading(trimmed) || isBracketedMarker(trimmed) {
				headers[trimmed] = true
			}
		}
	}
	return headers
}

// isMarkdownHeading reports whether line is an ATX markdown heading: 1-6 '#'
// followed by a space. The space is required so "#hashtag" is not a heading,
// and the depth cap keeps "#######..." out.
func isMarkdownHeading(line string) bool {
	depth := 0
	for depth < len(line) && line[depth] == '#' {
		depth++
	}
	if depth == 0 || depth > 6 {
		return false
	}
	return depth < len(line) && line[depth] == ' '
}

// isBracketedMarker reports whether line is a standalone bracketed marker
// ("[SESSION ARCHIVE]", "[SKILL — collab]"). Markers with trailing text are
// excluded: the block body does not follow a blank line, so stripping "until
// the next blank line" could remove real content.
func isBracketedMarker(line string) bool {
	return strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")
}

// stripInternalPromptEcho removes echoed internal prompt/context blocks from
// model content. A block is a header line (one of the lines injected this
// turn) plus all following lines up to and including the next blank line (or
// EOF). Text outside these blocks is preserved verbatim.
func stripInternalPromptEcho(content string, injectedBlocks []string) string {
	if content == "" {
		return content
	}
	headers := internalEchoHeaders(injectedBlocks)
	if len(headers) == 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	for _, line := range lines {
		if skipping {
			if strings.TrimSpace(line) == "" {
				skipping = false
			}
			continue
		}
		if headers[strings.TrimSpace(line)] {
			skipping = true
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func hasValidToolCalls(calls []ModelToolCall) bool {
	for _, call := range calls {
		if call.Function.Name != "" {
			return true
		}
	}
	return false
}

// normalizeDSMLValue cleans up a parameter value extracted from DSML tokens.
// The model may insert line breaks within values due to output line-length limits,
// producing broken paths like "/path/to/ar\nchive/Foo.\njava".
// For single-line values (paths, patterns), we collapse internal whitespace.
// For multi-line values (commands), we preserve intentional newlines.
func normalizeDSMLValue(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) == 1 {
		return trimmed
	}

	collapsed := strings.TrimSpace(strings.Join(lines, ""))
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	rejoined := strings.Join(lines, "\n")

	if !strings.Contains(collapsed, " ") && !strings.Contains(collapsed, "\n") {
		return collapsed
	}
	return rejoined
}

// Regex patterns for DSML detection and parsing.
// Supports full-width vertical bar ｜ (U+FF5C), ASCII pipe |, and mixed variants.
// Also handles incomplete/truncated DSML blocks (missing closing tag).
var (
	// Detection: broad check for any DSML-like content (full-width OR ASCII)
	dsmlDetectRe = regexp.MustCompile(`[<＜][｜|]+DSML[｜|]+`)

	// Full-width complete block: <｜+DSML｜+tool_calls>...</｜+DSML｜+tool_calls>
	dsmlBlockRe = regexp.MustCompile(`(?s)[<＜][｜|]+DSML[｜|]+tool_calls[>＞]\s*(.*?)\s*[<＜]/[｜|]+DSML[｜|]+tool_calls[>＞]`)

	// Full-width incomplete block (truncated, no closing tag): <｜+DSML｜+tool_calls>...EOF
	dsmlIncompleteBlockRe = regexp.MustCompile(`(?s)[<＜][｜|]+DSML[｜|]+tool_calls[>＞].*$`)

	// ASCII pipe variant complete: <||DSML||tool_calls>...</||DSML||tool_calls>
	dsmlAsciiBlockRe = regexp.MustCompile(`(?s)<\|+DSML\|+tool_calls>\s*(.*?)\s*</\|+DSML\|+tool_calls>`)

	// ASCII pipe variant incomplete
	dsmlAsciiIncompleteRe = regexp.MustCompile(`(?s)<\|+DSML\|+tool_calls>.*$`)

	// Invoke and parameter patterns (support both pipe variants)
	dsmlInvokeRe = regexp.MustCompile(`(?s)[<＜][｜|]+DSML[｜|]+invoke\s+name="([^"]+)"[>＞]\s*(.*?)\s*[<＜]/[｜|]+DSML[｜|]+invoke[>＞]`)
	dsmlParamRe  = regexp.MustCompile(`(?s)[<＜][｜|]+DSML[｜|]+parameter\s+name="([^"]+)"[^>＞]*[>＞](.*?)[<＜]/[｜|]+DSML[｜|]+parameter[>＞]`)
)
