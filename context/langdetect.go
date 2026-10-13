package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// detectUserLanguage / classifyTextLanguage are gone: the model-facing prompt
// is English-only, so the session language no longer selects a prompt.
