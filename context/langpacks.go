package context

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed langpacks/*.md
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
func GetLangPacks(hits []LanguageHit) string {
	var b strings.Builder
	b.WriteString("Detected project languages (heuristic; the file that triggered each — trust the code over this list):\n")
	if len(hits) == 0 {
		b.WriteString("- none detected (generic rules only)\n")
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
		if p := langPack(lang); p != "" {
			b.WriteString("\n" + p + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// langPack reads the pack for one language. There is a single language pack
// per project language; the prompt is English-only, so no per-session variant
// is selected.
func langPack(lang Language) string {
	data, err := langpackFS.ReadFile("langpacks/" + string(lang) + ".md")
	if err != nil {
		return ""
	}
	return string(data)
}
