package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectLanguages_Polyglot: a repository with several manifests reports all
// of them, each with the marker file behind the hit — a first-match-wins probe
// would report only the first.
func TestDetectLanguages_Polyglot(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"go.mod", "tsconfig.json", "pyproject.toml"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	hits := DetectLanguages(dir)
	want := map[Language]string{
		LangGo:         "go.mod",
		LangTypeScript: "tsconfig.json",
		LangPython:     "pyproject.toml",
	}
	if len(hits) != len(want) {
		t.Fatalf("got %d hits, want %d: %+v", len(hits), len(want), hits)
	}
	for _, h := range hits {
		if want[h.Lang] != h.Marker {
			t.Errorf("hit %s marker = %q, want %q", h.Lang, h.Marker, want[h.Lang])
		}
	}
}

// TestDetectLanguages_Empty: no manifests means no hits — the generic pack
// covers that case.
func TestDetectLanguages_Empty(t *testing.T) {
	if hits := DetectLanguages(t.TempDir()); len(hits) != 0 {
		t.Errorf("expected no hits, got %+v", hits)
	}
	if hits := DetectLanguages(""); len(hits) != 0 {
		t.Errorf("empty projectRoot should yield no hits, got %+v", hits)
	}
}

// TestGetLangPacks_IncludesGenericAndDetected: the cross-language baseline is
// always present, plus one pack per detected language, plus the detection facts.
func TestGetLangPacks_IncludesGenericAndDetected(t *testing.T) {
	hits := []LanguageHit{
		{Lang: LangGo, Marker: "go.mod"},
		{Lang: LangTypeScript, Marker: "tsconfig.json"},
	}
	got := GetLangPacks(hits, "")

	for _, want := range []string{
		"Detected project languages",
		"- go (go.mod)",
		"- typescript (tsconfig.json)",
		"# Generic Language Pack",
		"# Go Language Pack",
		"# TypeScript Language Pack",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("pack block missing %q:\n%s", want, got)
		}
	}
}

// TestGetLangPacks_GenericOnlyWhenNothingDetected: without a detection the block
// still carries the cross-language baseline, and no language-specific rules.
func TestGetLangPacks_GenericOnlyWhenNothingDetected(t *testing.T) {
	got := GetLangPacks(nil, "")
	if !strings.Contains(got, "# Generic Language Pack") {
		t.Errorf("generic pack must always be present:\n%s", got)
	}
	if strings.Contains(got, "# Go Language Pack") {
		t.Errorf("no language pack should be injected when none was detected:\n%s", got)
	}
}

// TestGetLangPacks_ChineseVariant: a Chinese session gets the Chinese packs
// where they exist (generic, go) and the English pack otherwise.
func TestGetLangPacks_ChineseVariant(t *testing.T) {
	got := GetLangPacks([]LanguageHit{{Lang: LangGo, Marker: "go.mod"}}, "中文")
	for _, want := range []string{"检测到的项目语言", "# 通用语言包", "# Go 语言包"} {
		if !strings.Contains(got, want) {
			t.Errorf("Chinese pack block missing %q:\n%s", want, got)
		}
	}
}

// TestGetLangPacks_NoDuplicateGeneric: the generic pack appears exactly once
// even when it is the only pack in play.
func TestGetLangPacks_NoDuplicateGeneric(t *testing.T) {
	got := GetLangPacks([]LanguageHit{{Lang: LangGeneric}}, "")
	if n := strings.Count(got, "# Generic Language Pack"); n != 1 {
		t.Errorf("generic pack appears %d times, want 1:\n%s", n, got)
	}
}
