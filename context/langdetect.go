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

// LanguageHit records a detected project language and the file that revealed
// it. The marker is carried into the prompt so the model can see the basis of
// the heuristic and correct course when it is wrong.
type LanguageHit struct {
	Lang   Language
	Marker string
}

// DetectLanguages returns every language detected at projectRoot, in priority
// order, together with the marker file behind each hit.
//
// A first-match-wins probe reports exactly one language, which is wrong for
// polyglot repositories (a Go service with a TypeScript frontend): the model
// then gets one language's rules and no hint that another language is in play.
// Reporting all hits lets every detected language's pack be injected.
//
// Detection is a heuristic on manifest files — the returned Marker names the
// file that triggered each hit so the prompt can state its basis.
func DetectLanguages(projectRoot string) []LanguageHit {
	if projectRoot == "" {
		return nil
	}
	var hits []LanguageHit
	add := func(lang Language, marker string) {
		for _, h := range hits {
			if h.Lang == lang {
				return
			}
		}
		hits = append(hits, LanguageHit{Lang: lang, Marker: marker})
	}

	if exists(filepath.Join(projectRoot, "go.mod")) {
		add(LangGo, "go.mod")
	}
	switch {
	case exists(filepath.Join(projectRoot, "tsconfig.json")):
		add(LangTypeScript, "tsconfig.json")
	case packageHasTypeScript(filepath.Join(projectRoot, "package.json")):
		add(LangTypeScript, "package.json")
	}
	for _, m := range []string{"pyproject.toml", "requirements.txt", "setup.py"} {
		if exists(filepath.Join(projectRoot, m)) {
			add(LangPython, m)
			break
		}
	}
	if exists(filepath.Join(projectRoot, "Cargo.toml")) {
		add(LangRust, "Cargo.toml")
	}
	for _, m := range []string{"pom.xml", "build.gradle", "build.gradle.kts"} {
		if exists(filepath.Join(projectRoot, m)) {
			add(LangJava, m)
			break
		}
	}
	return hits
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

// detectUserLanguage determines the response language from the user's FIRST
// message in the session. It deliberately ignores later messages so that
// short English confirmations ("ok", "yes", "确认") or English-heavy tool
// output cannot flip the language away from what the user originally wrote.
// The result is locked for the whole session (see ContextAssembler.userLang).
//
// Callers only invoke this once history holds a non-empty user message
// (see hasFirstUserMessage), so LangUnset is unreachable in practice.
func detectUserLanguage(history []engine.Message) engine.UserLanguage {
	for i := 0; i < len(history); i++ {
		if history[i].Role == "user" && strings.TrimSpace(history[i].Content) != "" {
			return classifyTextLanguage(history[i].Content)
		}
	}
	return engine.LangUnset
}

func classifyTextLanguage(text string) engine.UserLanguage {
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
	// Aligned with engine.msgIsChinese: any CJK character → Chinese.
	// This ensures ContextBuilder and Engine use the same detection threshold,
	// preventing mixed-language output when the user's message contains both
	// Chinese and English (e.g., "帮我 fix the bug").
	if cjk > 0 {
		return engine.LangChinese
	}
	runes := []rune(text)
	if len(runes) > 0 {
		if unicode.Is(unicode.Hiragana, runes[0]) || unicode.Is(unicode.Katakana, runes[0]) {
			return engine.LangJapanese
		}
	}
	return engine.LangEnglish
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
