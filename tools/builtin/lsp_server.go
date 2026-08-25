package builtin

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// lspServerSpec describes how to launch one language server subprocess.
// DeepAct speaks to it over stdin/stdout using the LSP JSON-RPC protocol.
type lspServerSpec struct {
	// Language is the LSP languageId sent in textDocument/didOpen, e.g. "go".
	// It is also used to key the per-(session,language) subprocess.
	Language string `json:"language"`
	// Command is the executable name resolved from PATH, e.g. "gopls".
	Command string `json:"command"`
	// Args are the default argv passed to the server. Servers that speak LSP
	// over stdio differ: gopls uses `serve`, typescript-language-server and
	// pyright use `--stdio`, rust-analyzer defaults to stdio.
	Args []string `json:"args,omitempty"`
	// InitOpts, when set, is merged into the initialize request's
	// "initializationOptions". Most servers accept an empty object; use this for
	// servers that require it (e.g. pyright's python.analysis settings).
	InitOpts json.RawMessage `json:"init_opts,omitempty"`
}

// LSPCommand is a user-supplied override for one language (the [lsp] config
// section). It replaces the default command/args/languageId for that server.
type LSPCommand struct {
	Command  string   `json:"command"`
	Args     []string `json:"args,omitempty"`
	Language string   `json:"language,omitempty"` // optional override of the LSP languageId
}

// defaultLSPByExt maps file extension -> server spec. Keys are lowercase
// (including the leading dot, e.g. ".go"). This is the pristine default table;
// each LSPTool instance copies it and layers config overrides on top.
var defaultLSPByExt = map[string]lspServerSpec{
	".go":   {Language: "go", Command: "gopls", Args: []string{"serve"}},
	".ts":   {Language: "typescript", Command: "typescript-language-server", Args: []string{"--stdio"}},
	".tsx":  {Language: "typescriptreact", Command: "typescript-language-server", Args: []string{"--stdio"}},
	".js":   {Language: "javascript", Command: "typescript-language-server", Args: []string{"--stdio"}},
	".jsx":  {Language: "javascriptreact", Command: "typescript-language-server", Args: []string{"--stdio"}},
	".mjs":  {Language: "javascript", Command: "typescript-language-server", Args: []string{"--stdio"}},
	".cjs":  {Language: "javascript", Command: "typescript-language-server", Args: []string{"--stdio"}},
	".py":   {Language: "python", Command: "pyright-langserver", Args: []string{"--stdio"}},
	".rs":   {Language: "rust", Command: "rust-analyzer", Args: []string{}},
	".json": {Language: "json", Command: "vscode-json-languageserver", Args: []string{"--stdio"}},
	".yaml": {Language: "yaml", Command: "yaml-language-server", Args: []string{"--stdio"}},
	".yml":  {Language: "yaml", Command: "yaml-language-server", Args: []string{"--stdio"}},
	".toml": {Language: "toml", Command: "taplo", Args: []string{}},
	".md":   {Language: "markdown", Command: "marksman", Args: []string{}},
	".sh":   {Language: "shellscript", Command: "bash-language-server", Args: []string{"start"}},
	".bash": {Language: "shellscript", Command: "bash-language-server", Args: []string{"start"}},
	".c":    {Language: "c", Command: "clangd", Args: []string{}},
	".h":    {Language: "c", Command: "clangd", Args: []string{}},
	".cpp":  {Language: "cpp", Command: "clangd", Args: []string{}},
	".cc":   {Language: "cpp", Command: "clangd", Args: []string{}},
	".cxx":  {Language: "cpp", Command: "clangd", Args: []string{}},
	".hpp":  {Language: "cpp", Command: "clangd", Args: []string{}},
	".css":  {Language: "css", Command: "vscode-css-language-server", Args: []string{"--stdio"}},
	".html": {Language: "html", Command: "vscode-html-language-server", Args: []string{"--stdio"}},
}

// buildLSPServers returns a copy of the default extension->spec table with any
// [lsp] config overrides applied. Overrides are keyed by LSP language; every
// extension currently mapped to that language picks up the new command/args.
func buildLSPServers(overrides map[string]LSPCommand) map[string]lspServerSpec {
	servers := make(map[string]lspServerSpec, len(defaultLSPByExt))
	for ext, spec := range defaultLSPByExt {
		servers[ext] = spec
	}
	for lang, cmd := range overrides {
		if cmd.Command == "" {
			continue
		}
		if cmd.Language == "" {
			cmd.Language = lang
		}
		for ext, spec := range servers {
			if spec.Language == lang {
				spec.Command = cmd.Command
				spec.Args = cmd.Args
				spec.Language = cmd.Language
				servers[ext] = spec
			}
		}
	}
	return servers
}

// resolveLSPByExt returns the server spec for a file path by its extension.
func resolveLSPByExt(servers map[string]lspServerSpec, path string) (lspServerSpec, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	spec, ok := servers[ext]
	return spec, ok
}

// resolveLSPByLanguage returns the server spec for a named LSP language.
func resolveLSPByLanguage(servers map[string]lspServerSpec, language string) (lspServerSpec, bool) {
	for _, spec := range servers {
		if spec.Language == language {
			return spec, true
		}
	}
	return lspServerSpec{}, false
}

// knownLanguages returns the sorted set of configured LSP languageIds. Used to
// fan out workspace/symbol across every server without needing a file first.
func knownLanguages(servers map[string]lspServerSpec) []string {
	seen := make(map[string]struct{}, len(servers))
	for _, spec := range servers {
		seen[spec.Language] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for lang := range seen {
		out = append(out, lang)
	}
	sort.Strings(out)
	return out
}
