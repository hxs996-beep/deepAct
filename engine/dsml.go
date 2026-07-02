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

// internalPromptBlockPrefixes lists the leading markers of context/prompt blocks
// that DeepAct injects into the model's input (Block B, Block S, TASK REMINDER,
// read-history hint, language pack, ...). DeepSeek sometimes echoes these back
// in its content. Such echoes must never reach the user or be written back into
// history (they pollute subsequent turns and surface as a fake "完成" summary).
//
// These markers are structural and unambiguous — prefix matching is safe because
// they don't appear in natural conversation (e.g. "# Block B:", "## Environment").
var internalPromptBlockPrefixes = []string{
	"# Block B: Runtime Context",
	"# Block B：运行时上下文",
	"# Block S: Session Context",
	"# Block S：会话上下文",
	"## Task State",
	"## 任务状态",
	"## Environment",
	"## 环境",
	"# Language Pack",
	"[TASK REMINDER]",
	"<TASK REMINDER>",
	"</TASK REMINDER>",
	"## Recent Actions",
	"## Reminder on tool usage",
}

// internalPromptExactHeaders lists natural-language section headers that are
// common enough to appear in the model's legitimate answers. These MUST match
// the entire trimmed line (or line followed by "：" / ":") — prefix matching
// would strip real answers like "已读文件显示代码结构如下…".
var internalPromptExactHeaders = []string{
	"Files already read",
	"已读文件",
}

func isInternalPromptHeader(line string) bool {
	trimmed := strings.TrimSpace(line)

	// Exact-match headers: natural-language phrases that could appear in answers.
	for _, p := range internalPromptExactHeaders {
		if trimmed == p {
			return true
		}
		if strings.HasPrefix(trimmed, p+"：") {
			return true
		}
		if strings.HasPrefix(trimmed, p+":") {
			return true
		}
	}

	// Prefix-match headers: structural markers safe to prefix-match.
	for _, p := range internalPromptBlockPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

// stripInternalPromptEcho removes echoed internal prompt/context blocks from
// model content. A block is a header line (matching internalPromptBlockPrefixes)
// plus all following lines up to and including the next blank line (or EOF).
// Text outside these blocks is preserved verbatim.
func stripInternalPromptEcho(content string) string {
	if content == "" {
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
		if isInternalPromptHeader(line) {
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
