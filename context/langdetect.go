package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/deepact/deepact/engine"
)

type Language string

const (
	LangGo         Language = "go"
	LangTypeScript Language = "typescript"
	LangPython     Language = "python"
	LangRust       Language = "rust"
	LangJava       Language = "java"
	LangGeneric    Language = "generic"
)

func DetectLanguage(projectRoot string) Language {
	if projectRoot == "" {
		return LangGeneric
	}
	if exists(filepath.Join(projectRoot, "go.mod")) {
		return LangGo
	}
	if exists(filepath.Join(projectRoot, "tsconfig.json")) || packageHasTypeScript(filepath.Join(projectRoot, "package.json")) {
		return LangTypeScript
	}
	if exists(filepath.Join(projectRoot, "pyproject.toml")) || exists(filepath.Join(projectRoot, "requirements.txt")) || exists(filepath.Join(projectRoot, "setup.py")) {
		return LangPython
	}
	if exists(filepath.Join(projectRoot, "Cargo.toml")) {
		return LangRust
	}
	if exists(filepath.Join(projectRoot, "pom.xml")) || exists(filepath.Join(projectRoot, "build.gradle")) || exists(filepath.Join(projectRoot, "build.gradle.kts")) {
		return LangJava
	}
	return LangGeneric
}

func exists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func packageHasTypeScript(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var pkg map[string]any
	if err := json.Unmarshal(data, &pkg); err == nil {
		for _, section := range []string{"dependencies", "devDependencies", "peerDependencies"} {
			if deps, ok := pkg[section].(map[string]any); ok {
				if _, ok := deps["typescript"]; ok {
					return true
				}
			}
		}
	}
	return strings.Contains(string(data), "\"typescript\"")
}

func detectUserLanguage(history []engine.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" && strings.TrimSpace(history[i].Content) != "" {
			return classifyTextLanguage(history[i].Content)
		}
	}
	return ""
}

func classifyTextLanguage(text string) string {
	// Strip markdown code fences (```...```) and inline code (`...`) before
	// counting, so that pasted code snippets don't skew language detection.
	text = stripMarkdownCode(text)

	var cjk, latin int
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			cjk++
		} else if unicode.Is(unicode.Latin, r) {
			latin++
		}
	}
	if cjk > 0 && cjk >= latin/3 {
		return "中文"
	}
	runes := []rune(text)
	if len(runes) > 0 {
		if unicode.Is(unicode.Hiragana, runes[0]) || unicode.Is(unicode.Katakana, runes[0]) {
			return "日本語"
		}
	}
	return ""
}

// stripMarkdownCode removes markdown fenced code blocks (```...```) and inline
// code spans (`...`) from s, returning only the natural-language content.
func stripMarkdownCode(s string) string {
	// Phase 1: strip fenced code blocks (```...```), including language tag.
	var buf strings.Builder
	i := 0
	for i < len(s) {
		if i+2 < len(s) && s[i] == '`' && s[i+1] == '`' && s[i+2] == '`' {
			// Skip opening fence
			i += 3
			// Skip until end of line (language tag)
			for i < len(s) && s[i] != '\n' {
				i++
			}
			// Skip until closing fence
			for i+2 < len(s) {
				if s[i] == '`' && s[i+1] == '`' && s[i+2] == '`' {
					i += 3
					break
				}
				i++
			}
			continue
		}
		buf.WriteByte(s[i])
		i++
	}
	s = buf.String()

	// Phase 2: strip inline code spans (`...`).
	var buf2 strings.Builder
	inInline := false
	for _, r := range s {
		if r == '`' {
			inInline = !inInline
			continue
		}
		if !inInline {
			buf2.WriteRune(r)
		}
	}
	return buf2.String()
}
