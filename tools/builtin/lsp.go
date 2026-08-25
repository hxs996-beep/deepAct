package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deepact/deepact/tools"
)

const lspTimeOut = 30 * time.Second

// LSPTool provides code intelligence across many languages by driving a
// per-language LSP server (gopls, typescript-language-server, pyright,
// rust-analyzer, ...). The server table is copied from the defaults and then
// layered with any [lsp] config overrides.
type LSPTool struct {
	servers map[string]lspServerSpec // extension -> spec
}

func NewLSPTool() *LSPTool {
	return &LSPTool{servers: buildLSPServers(nil)}
}

// NewLSPToolWithOverrides builds the tool with user-supplied server overrides
// keyed by LSP language (see buildLSPServers). The override map is owned by the
// caller and copied in, so later mutation does not affect the tool.
func NewLSPToolWithOverrides(overrides map[string]LSPCommand) *LSPTool {
	return &LSPTool{servers: buildLSPServers(overrides)}
}

func (t *LSPTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name: "lsp",
		Description: `Interact with a language server for precise code intelligence. The server is
selected automatically from the file extension (gopls for Go, typescript-language-server
for TS/JS, pyright-langserver for Python, rust-analyzer for Rust, clangd for C/C++, and more).

Supported operations:
- goToDefinition: Find where a symbol is defined (requires file_path, line, character)
- findReferences: Find all references to a symbol (requires file_path, line, character)
- hover: Get type information and documentation for a symbol (requires file_path, line, character)
- documentSymbol: List all symbols (types, functions, methods) in a file (requires file_path)
- workspaceSymbol: Search for symbols across the entire workspace (requires query; optionally hint language)
- goToImplementation: Find implementations of an interface (requires file_path, line, character)
- incomingCalls: Find all functions that call the function at a position (requires file_path, line, character)
- outgoingCalls: Find all functions called by the function at a position (requires file_path, line, character)

For operations requiring a position (goToDefinition, findReferences, hover, etc.):
line and character are 1-based (as shown in editors/cat -n output).

Use this tool INSTEAD of reading entire files when you need to:
- Find where a function/type is defined
- Look up type information
- Find all callers of a function
- Explore symbol structure
- Search for symbols by name`,
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "operation": {
      "type": "string",
      "enum": ["goToDefinition", "findReferences", "hover", "documentSymbol", "workspaceSymbol", "goToImplementation", "incomingCalls", "outgoingCalls"],
      "description": "The LSP operation to perform"
    },
    "file_path": {
      "type": "string",
      "description": "Absolute path to the file (required for goToDefinition, findReferences, hover, documentSymbol, goToImplementation, incomingCalls, outgoingCalls; used to choose the language server)"
    },
    "line": {
      "type": "integer",
      "description": "Line number (1-based, as shown in editors) — required for position-based operations"
    },
    "character": {
      "type": "integer",
      "description": "Character offset (1-based, as shown in editors) — required for position-based operations"
    },
    "query": {
      "type": "string",
      "description": "Symbol name to search for (required for workspaceSymbol)"
    },
    "language": {
      "type": "string",
      "description": "Optional LSP language hint for workspaceSymbol when no file_path is given (e.g. 'go', 'typescript', 'python', 'rust'). If omitted, all configured servers are queried."
    }
  },
  "required": ["operation"]
}`),
	}
}

type lspInput struct {
	Operation string `json:"operation"`
	FilePath  string `json:"file_path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
	Query     string `json:"query"`
	Language  string `json:"language"` // optional hint for workspaceSymbol
}

func (t *LSPTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload lspInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Operation = strings.TrimSpace(payload.Operation)
	if payload.Operation == "" {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: "operation is required"}, nil
	}

	// Resolve file_path if provided
	var safePath string
	if payload.FilePath != "" {
		var err error
		safePath, err = resolveSafePath(ctx.WorkDir, payload.FilePath)
		if err != nil {
			return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
		}
	}

	// Validate operation-specific requirements
	switch payload.Operation {
	case "goToDefinition", "findReferences", "hover", "goToImplementation", "incomingCalls", "outgoingCalls":
		if payload.FilePath == "" {
			return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("%s requires file_path", payload.Operation)}, nil
		}
		if payload.Line < 1 {
			return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("%s requires line (1-based)", payload.Operation)}, nil
		}
		if payload.Character < 1 {
			return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("%s requires character (1-based)", payload.Operation)}, nil
		}
	case "documentSymbol":
		if payload.FilePath == "" {
			return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: "documentSymbol requires file_path"}, nil
		}
	case "workspaceSymbol":
		if payload.Query == "" {
			return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: "workspaceSymbol requires query"}, nil
		}
	}

	// Per-call: timeout for this specific operation (the server process is NOT killed when this cancels)
	lspCtx, cancel := context.WithTimeout(context.Background(), lspTimeOut)
	defer cancel()

	// workspaceSymbol needs no file, so it may fan out across languages.
	if payload.Operation == "workspaceSymbol" {
		formatted, err := t.workspaceSymbol(lspCtx, ctx, payload)
		if err != nil {
			return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, nil
		}
		return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: formatted}, nil
	}

	// All remaining operations target one file's language server.
	spec, ok := resolveLSPByExt(t.servers, safePath)
	if !ok {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("no LSP server configured for file %s (unknown extension)", safePath)}, nil
	}
	session, err := globalLSPManager.getOrCreate(ctx.SessionID, ctx.WorkDir, spec)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("LSP session (%s): %v", spec.Language, err)}, err
	}

	// Execute the requested operation
	var result json.RawMessage
	switch payload.Operation {
	case "goToDefinition":
		result, err = session.goToDefinition(lspCtx, safePath, payload.Line, payload.Character)
	case "findReferences":
		result, err = session.findReferences(lspCtx, safePath, payload.Line, payload.Character, true)
	case "hover":
		result, err = session.hover(lspCtx, safePath, payload.Line, payload.Character)
	case "documentSymbol":
		result, err = session.documentSymbol(lspCtx, safePath)
	case "goToImplementation":
		result, err = session.goToImplementation(lspCtx, safePath, payload.Line, payload.Character)
	case "incomingCalls":
		result, err = session.incomingCalls(lspCtx, safePath, payload.Line, payload.Character)
	case "outgoingCalls":
		result, err = session.outgoingCalls(lspCtx, safePath, payload.Line, payload.Character)
	default:
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("unknown operation: %s", payload.Operation)}, nil
	}

	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("%s: %v", payload.Operation, err)}, nil
	}

	formatted := formatLSPResult(payload.Operation, safePath, result)
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: formatted}, nil
}

// workspaceSymbol resolves a symbol by name across the configured language
// servers. When the model provides a language hint, only that server is queried;
// otherwise every configured server is consulted and results are deduplicated
// by (name, file). Servers whose binary is missing are skipped with a note.
func (t *LSPTool) workspaceSymbol(ctx context.Context, toolCtx tools.ToolContext, payload lspInput) (string, error) {
	langs := knownLanguages(t.servers)
	if payload.Language != "" {
		if _, ok := resolveLSPByLanguage(t.servers, payload.Language); !ok {
			return "", fmt.Errorf("no LSP server configured for language %q", payload.Language)
		}
		langs = []string{payload.Language}
	}

	var all []lspSymbol
	var skipped []string
	for _, lang := range langs {
		spec, ok := resolveLSPByLanguage(t.servers, lang)
		if !ok {
			continue
		}
		session, err := globalLSPManager.getOrCreate(toolCtx.SessionID, toolCtx.WorkDir, spec)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", lang, err))
			continue
		}
		raw, err := session.workspaceSymbol(ctx, payload.Query)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", lang, err))
			continue
		}
		var syms []lspSymbol
		if err := json.Unmarshal(raw, &syms); err != nil {
			// Some servers return a single element or nothing.
			var one lspSymbol
			if err2 := json.Unmarshal(raw, &one); err2 == nil && one.Name != "" {
				syms = []lspSymbol{one}
			} else {
				skipped = append(skipped, fmt.Sprintf("%s (unexpected result)", lang))
				continue
			}
		}
		all = append(all, syms...)
	}

	// Dedupe by (name, file) because the same symbol can surface from several servers.
	seen := make(map[string]struct{}, len(all))
	dedup := all[:0]
	for _, sym := range all {
		key := sym.Name + "\x00" + sym.Location.URI
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		dedup = append(dedup, sym)
	}

	var b strings.Builder
	b.WriteString("LSP workspaceSymbol result:\n")
	if len(skipped) > 0 {
		b.WriteString(fmt.Sprintf("  (servers skipped: %s)\n", strings.Join(skipped, ", ")))
	}
	if len(dedup) == 0 {
		b.WriteString("  (no results)\n")
		return b.String(), nil
	}
	raw, err := json.Marshal(dedup)
	if err != nil {
		return "", err
	}
	return formatLSPResult("workspaceSymbol", "", raw), nil
}

func formatLSPResult(operation string, filePath string, result json.RawMessage) string {
	if result == nil {
		return "(no results)"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("LSP %s result:\n", operation))

	switch operation {
	case "goToDefinition", "goToImplementation":
		// Result is Location | Location[] | LocationLink[]
		var locations []lspLocation
		if err := json.Unmarshal(result, &locations); err != nil {
			// Try single location
			var loc lspLocation
			if err2 := json.Unmarshal(result, &loc); err2 != nil {
				b.WriteString(string(result))
				return b.String()
			}
			locations = []lspLocation{loc}
		}
		for _, loc := range locations {
			path := strings.TrimPrefix(loc.URI, "file://")
			b.WriteString(fmt.Sprintf("  %s:%d:%d\n",
				path, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
		}

	case "hover":
		var hover lspHover
		if err := json.Unmarshal(result, &hover); err != nil {
			b.WriteString(string(result))
			return b.String()
		}
		// Hover contents can be MarkupContent | MarkedString | MarkedString[]
		b.WriteString(formatHoverContents(hover.Contents, 0))

	case "documentSymbol":
		// Result is SymbolInformation[] | DocumentSymbol[]
		var raw []json.RawMessage
		if err := json.Unmarshal(result, &raw); err != nil {
			b.WriteString(string(result))
			return b.String()
		}
		for _, r := range raw {
			b.WriteString(formatDocumentSymbol(r, 0))
		}

	case "workspaceSymbol":
		var symbols []lspSymbol
		if err := json.Unmarshal(result, &symbols); err != nil {
			b.WriteString(string(result))
			return b.String()
		}
		for _, sym := range symbols {
			path := strings.TrimPrefix(sym.Location.URI, "file://")
			b.WriteString(fmt.Sprintf("  %s (%s:%d:%d)",
				sym.Name, path,
				sym.Location.Range.Start.Line+1,
				sym.Location.Range.Start.Character+1))
			if sym.ContainerName != "" {
				b.WriteString(fmt.Sprintf(" — %s", sym.ContainerName))
			}
			b.WriteString("\n")
		}

	case "findReferences":
		var locations []lspLocation
		if err := json.Unmarshal(result, &locations); err != nil {
			b.WriteString(string(result))
			return b.String()
		}
		// Group by file
		fileGroups := make(map[string][]lspLocation)
		for _, loc := range locations {
			path := strings.TrimPrefix(loc.URI, "file://")
			fileGroups[path] = append(fileGroups[path], loc)
		}
		for path, locs := range fileGroups {
			b.WriteString(fmt.Sprintf("  %s:\n", path))
			for _, loc := range locs {
				b.WriteString(fmt.Sprintf("    line %d\n", loc.Range.Start.Line+1))
			}
		}

	case "incomingCalls", "outgoingCalls":
		// Result is CallHierarchyIncomingCall[] | CallHierarchyOutgoingCall[]
		var calls []json.RawMessage
		if err := json.Unmarshal(result, &calls); err != nil {
			// Try with the proper type
			b.WriteString(string(result))
			return b.String()
		}
		for _, call := range calls {
			b.WriteString(formatCallHierarchyCall(call))
		}
	}

	return b.String()
}

func formatHoverContents(contents json.RawMessage, indent int) string {
	prefix := strings.Repeat("  ", indent)

	// Try MarkupContent first (most common)
	var markup struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(contents, &markup); err == nil && markup.Value != "" {
		return prefix + markup.Value + "\n"
	}

	// Try array of MarkedString
	var arr []json.RawMessage
	if err := json.Unmarshal(contents, &arr); err == nil {
		var result strings.Builder
		for _, item := range arr {
			result.WriteString(formatHoverContents(item, indent))
		}
		return result.String()
	}

	// Try plain string
	var str string
	if err := json.Unmarshal(contents, &str); err == nil {
		return prefix + str + "\n"
	}

	return prefix + string(contents) + "\n"
}

func formatDocumentSymbol(raw json.RawMessage, indent int) string {
	prefix := strings.Repeat("  ", indent)

	// Try DocumentSymbol first (has children)
	var ds struct {
		Name           string            `json:"name"`
		Kind           int               `json:"kind"`
		Detail         string            `json:"detail,omitempty"`
		Range          json.RawMessage   `json:"range"`
		SelectionRange json.RawMessage   `json:"selectionRange"`
		Children       []json.RawMessage `json:"children,omitempty"`
	}
	if err := json.Unmarshal(raw, &ds); err == nil {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%s%s", prefix, ds.Name))
		if ds.Detail != "" {
			b.WriteString(fmt.Sprintf(" (%s)", ds.Detail))
		}
		b.WriteString("\n")
		for _, child := range ds.Children {
			b.WriteString(formatDocumentSymbol(child, indent+1))
		}
		return b.String()
	}

	// Try SymbolInformation
	var si struct {
		Name string `json:"name"`
		Kind int    `json:"kind"`
	}
	if err := json.Unmarshal(raw, &si); err == nil {
		return fmt.Sprintf("%s%s\n", prefix, si.Name)
	}

	return prefix + string(raw) + "\n"
}

func formatCallHierarchyCall(item json.RawMessage) string {
	var call struct {
		From struct {
			Name           string          `json:"name"`
			Kind           int             `json:"kind"`
			Detail         string          `json:"detail,omitempty"`
			URI            string          `json:"uri"`
			Range          json.RawMessage `json:"range"`
			SelectionRange json.RawMessage `json:"selectionRange"`
		} `json:"from"`
		FromRanges []struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
			End struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"end"`
		} `json:"fromRanges"`
	}
	if err := json.Unmarshal(item, &call); err != nil {
		return fmt.Sprintf("  %s\n", string(item))
	}

	path := strings.TrimPrefix(call.From.URI, "file://")
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %s (%s)", call.From.Name, path))
	if call.From.Detail != "" {
		b.WriteString(fmt.Sprintf(" — %s", call.From.Detail))
	}
	b.WriteString("\n")
	if len(call.FromRanges) > 0 {
		for _, r := range call.FromRanges[:min(3, len(call.FromRanges))] { // show up to 3 call sites
			b.WriteString(fmt.Sprintf("    at line %d\n", r.Start.Line+1))
		}
		if len(call.FromRanges) > 3 {
			b.WriteString(fmt.Sprintf("    ... and %d more\n", len(call.FromRanges)-3))
		}
	}
	return b.String()
}
