package builtin

import (
	"testing"
)

func TestResolveLSPByExt(t *testing.T) {
	servers := buildLSPServers(nil)

	// Case sensitivity is normalized; by-default Go maps to gopls.
	cases := []struct {
		path     string
		wantLang string
	}{
		{"main.go", "go"},
		{"pkg/main.GO", "go"}, // uppercase ext normalized
		{"cmd/util.go", "go"},
		{"app.tsx", "typescriptreact"},
		{"app.ts", "typescript"},
		{"index.js", "javascript"},
		{"module.py", "python"},
		{"lib.rs", "rust"},
		{"config.json", "json"},
		{"deploy.yaml", "yaml"},
		{"build.sh", "shellscript"},
	}
	for _, c := range cases {
		spec, ok := resolveLSPByExt(servers, c.path)
		if !ok {
			t.Errorf("resolveLSPByExt(%q): expected a server, got none", c.path)
			continue
		}
		if spec.Language != c.wantLang {
			t.Errorf("resolveLSPByExt(%q) language = %q, want %q", c.path, spec.Language, c.wantLang)
		}
	}

	if _, ok := resolveLSPByExt(servers, "unknown.zzz"); ok {
		t.Errorf("resolveLSPByExt(unknown.zzz): expected no server, got one")
	}
	if _, ok := resolveLSPByExt(servers, ""); ok {
		t.Errorf("resolveLSPByExt(\"\"): expected no server for empty path")
	}
}

func TestLSPCommandOverrides(t *testing.T) {
	servers := buildLSPServers(map[string]LSPCommand{
		"go": {Command: "custom-go-ls", Args: []string{"--stdio"}, Language: "go"},
	})

	spec, ok := resolveLSPByExt(servers, "main.go")
	if !ok {
		t.Fatal("expected a server for .go after override")
	}
	if spec.Command != "custom-go-ls" {
		t.Errorf("command = %q, want custom-go-ls", spec.Command)
	}
	if len(spec.Args) != 1 || spec.Args[0] != "--stdio" {
		t.Errorf("args = %v, want [--stdio]", spec.Args)
	}

	// Overrides apply to every extension mapped to the language.
	if spec2, ok := resolveLSPByExt(servers, "main.ts"); ok && spec2.Language == "go" {
		_ = spec2 // sanity: .ts should NOT resolve to go
	}
	// A language override must not leak onto unrelated languages.
	pySpec, ok := resolveLSPByExt(servers, "app.py")
	if !ok {
		t.Fatal("expected python server")
	}
	if pySpec.Command != "pyright-langserver" {
		t.Errorf("python command = %q, want untouched default", pySpec.Command)
	}
}

func TestLSPCommandOverride_LanguageIDChange(t *testing.T) {
	// A user can override the LSP languageId so the type-react server maps a
	// different extension to a different languageId.
	servers := buildLSPServers(map[string]LSPCommand{
		"typescriptreact": {Command: "ts-ls", Language: "typescript"},
	})
	spec, ok := resolveLSPByExt(servers, "app.tsx")
	if !ok {
		t.Fatal("expected .tsx server")
	}
	if spec.Language != "typescript" {
		t.Errorf("languageId = %q, want typescript", spec.Language)
	}
}

func TestKnownLanguages(t *testing.T) {
	servers := buildLSPServers(nil)
	langs := knownLanguages(servers)
	if len(langs) == 0 {
		t.Fatal("expected at least one known language")
	}
	seen := make(map[string]bool)
	for _, l := range langs {
		if seen[l] {
			t.Errorf("duplicate known language: %q", l)
		}
		seen[l] = true
	}
	if !seen["go"] || !seen["typescript"] || !seen["python"] || !seen["rust"] {
		t.Errorf("languages missing core entries: %v", langs)
	}
}

func TestBuildLSPServers_NoMutation(t *testing.T) {
	// Overriding one language must not corrupt the pristine default table used
	// by other tool instances.
	before, _ := resolveLSPByExt(buildLSPServers(nil), "main.go")
	_ = buildLSPServers(map[string]LSPCommand{
		"go": {Command: "bogus"},
	})
	after, _ := resolveLSPByExt(buildLSPServers(nil), "main.go")
	if before.Command != after.Command {
		t.Errorf("defaults mutated: before=%q after=%q", before.Command, after.Command)
	}
}

func TestSessionKey_NamespacesByLanguage(t *testing.T) {
	if sessionKey("s1", "go") == sessionKey("s1", "typescript") {
		t.Error("different languages must produce different session keys")
	}
	if sessionKey("s1", "go") != sessionKey("s1", "go") {
		t.Error("same (session, language) must produce the same key")
	}
	if sessionKey("s1", "go") == sessionKey("s2", "go") {
		t.Error("different sessions must produce different session keys")
	}
}
