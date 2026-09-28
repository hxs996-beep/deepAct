package context

import (
	"embed"
	"fmt"
	"strings"

	"github.com/deepact/deepact/engine"
)

//go:embed langpacks/*.md langpacks/zh/*.md
var langpackFS embed.FS

// GetLangPacks assembles the language-rules block for a project: a header
// stating the detected languages and the marker file behind each detection,
// then the cross-language generic pack, then one pack per detected language.
//
// The generic pack is always included — it carries the cross-language baseline
// (consistent naming, explicit error handling, no magic numbers, smallest
// change) that the language-specific packs do not repeat, so it must not be
// replaced by them.
//
// Detection is a heuristic, so the marker is stated explicitly: the model can
// see the basis and trust the code over the list when the guess is wrong (e.g.
// a Go repository whose task is actually in its TypeScript frontend).
func GetLangPacks(hits []LanguageHit, userLang engine.UserLanguage) string {
	zh := userLang.IsChinese()
	var b strings.Builder
	if zh {
		b.WriteString("检测到的项目语言（启发式判定，括号内为触发文件；如与实际不符，以代码为准）：\n")
	} else {
		b.WriteString("Detected project languages (heuristic; the file that triggered each — trust the code over this list):\n")
	}
	if len(hits) == 0 {
		if zh {
			b.WriteString("- 未识别出特定语言（仅通用规则）\n")
		} else {
			b.WriteString("- none detected (generic rules only)\n")
		}
	}
	for _, h := range hits {
		if h.Marker == "" {
			fmt.Fprintf(&b, "- %s\n", h.Lang)
			continue
		}
		fmt.Fprintf(&b, "- %s (%s)\n", h.Lang, h.Marker)
	}

	order := []Language{LangGeneric}
	for _, h := range hits {
		if h.Lang != LangGeneric {
			order = append(order, h.Lang)
		}
	}
	seen := make(map[Language]bool, len(order))
	for _, lang := range order {
		if seen[lang] {
			continue
		}
		seen[lang] = true
		if p := langPack(lang, userLang); p != "" {
			b.WriteString("\n" + p + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// langPack reads the pack for one language, preferring the user's language
// variant and falling back to English when that variant does not exist (only
// generic and go have a Chinese translation).
func langPack(lang Language, userLang engine.UserLanguage) string {
	if userLang.IsChinese() {
		if data, err := langpackFS.ReadFile("langpacks/zh/" + string(lang) + ".md"); err == nil {
			return string(data)
		}
	}
	data, err := langpackFS.ReadFile("langpacks/" + string(lang) + ".md")
	if err != nil {
		return ""
	}
	return string(data)
}
