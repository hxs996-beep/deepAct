package engine

import (
	"encoding/json"
	"testing"
)

// testToolSpecs mirrors the production tool metadata for the tools engine tests
// exercise. The real table is pinned by tools/builtin/tool_metadata_test.go;
// engine test doubles use this so the engine reads the same declarations it
// reads in production (an undeclared tool is trusted and runs alone, which would
// make "repeat is not progress" assertions silently vacuous).
func testToolSpecs() []ModelTool {
	meta := func(name string, mutates, novelInfo, sequential bool) ModelTool {
		return ModelTool{
			Type:       "function",
			Function:   ModelToolFunction{Name: name},
			Mutates:    mutates,
			NovelInfo:  novelInfo,
			Sequential: sequential,
		}
	}
	return []ModelTool{
		meta("read", false, true, false),
		meta("read_multi", false, true, false),
		meta("grep", false, true, false),
		meta("glob", false, true, false),
		meta("lsp", false, true, false),
		meta("web_search", false, true, false),
		meta("fetch", false, true, false),
		meta("artifact", false, true, false),
		meta("edit", true, false, true),
		meta("write", true, false, true),
		meta("apply_patch", true, false, true),
		meta("revert", true, false, false),
		meta("bash", true, false, false),
		meta("skill_install", true, false, false),
		meta("remember_preference", true, false, false),
	}
}

func testSpecIndex() map[string]ModelTool {
	m := make(map[string]ModelTool)
	for _, s := range testToolSpecs() {
		m[s.Function.Name] = s
	}
	return m
}

// The batch/sequential split must come from the declaration, not from a
// hardcoded list: a tool the engine has never heard of runs ALONE (the safe
// direction), and bash stays batched because it says so.
func TestSplitCallsByExecution_FromMetadata(t *testing.T) {
	calls := []ToolCallRequest{
		{ID: "1", Name: "read"},
		{ID: "2", Name: "bash"},
		{ID: "3", Name: "edit"},
		{ID: "4", Name: "rename_file"}, // undeclared
		{ID: "5", Name: "some_mcp_tool"},
	}
	batch, sequential := splitCallsByExecution(calls, testSpecIndex())

	batchNames := toolCallNames(batch)
	if len(batchNames) != 2 || batchNames[0] != "read" || batchNames[1] != "bash" {
		t.Errorf("batch = %v, want [read bash]", batchNames)
	}
	seqNames := toolCallNames(sequential)
	if len(seqNames) != 3 {
		t.Fatalf("sequential = %v, want 3 calls (edit + 2 undeclared)", seqNames)
	}
	if seqNames[0] != "edit" {
		t.Errorf("declared Sequential tool must run alone, got %v", seqNames)
	}
	for _, n := range seqNames[1:] {
		if n != "rename_file" && n != "some_mcp_tool" {
			t.Errorf("undeclared tool %q must run alone", n)
		}
	}
}

func toolCallNames(calls []ToolCallRequest) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Name)
	}
	return out
}

// withExtra returns a copy of specs with one more declared tool, so a test can
// model a tool the built-ins do not have.
func withExtra(specs map[string]ModelTool, name string, mutates, novelInfo bool) map[string]ModelTool {
	out := make(map[string]ModelTool, len(specs)+1)
	for k, v := range specs {
		out[k] = v
	}
	out[name] = ModelTool{Type: "function", Function: ModelToolFunction{Name: name}, Mutates: mutates, NovelInfo: novelInfo}
	return out
}

// Every engine-side tool must be in the interception table, or its calls fall
// through to the registry: the engine has already answered the id, the registry
// answers it again with "tool not found", and the API rejects the duplicate
// response. This derives the requirement from the spec list the engine actually
// offers, so adding an inline tool without a table entry fails here.
func TestInterceptedTools_CoversEveryEngineInlineTool(t *testing.T) {
	e := &Engine{tools: stubToolExecutor{}}
	intercepted := interceptedToolNames()
	for _, spec := range e.toolSpecsWithHandoff() {
		name := spec.Function.Name
		if name == TaskCompleteToolName {
			// task_complete ends the turn before the split, so it never reaches
			// the batch; the split still refuses to dispatch it explicitly.
			continue
		}
		if !intercepted[name] {
			t.Errorf("engine-inline tool %q is missing from interceptedTools()", name)
		}
	}
}

// The table is the single source of truth: every entry needs a name and a
// handler, names must be unique, and the derived exclusion set must match it
// exactly (otherwise "answered by the engine" and "excluded from the batch"
// could disagree in either direction).
func TestInterceptedTools_TableIsConsistent(t *testing.T) {
	table := interceptedTools()
	seen := make(map[string]bool, len(table))
	for _, it := range table {
		if it.name == "" {
			t.Error("intercepted tool with an empty name")
			continue
		}
		if seen[it.name] {
			t.Errorf("intercepted tool %q listed twice", it.name)
		}
		seen[it.name] = true
		if it.handle == nil {
			t.Errorf("intercepted tool %q has no handler", it.name)
		}
	}

	set := interceptedToolNames()
	if len(set) != len(table) {
		t.Fatalf("name set has %d entries, table has %d", len(set), len(table))
	}
	for _, it := range table {
		if !set[it.name] {
			t.Errorf("%q missing from the derived name set", it.name)
		}
	}
}

// extractToolKey must see the same path aliases the read path sees
// (extractPathFromArgs accepts path / file_path / filePath). A tool call sent
// with an alias the repeat key cannot see loses its key, so the model never
// learns it is repeating itself — while the same file is tracked normally on the
// read path.
func TestExtractToolKey_PathAliases(t *testing.T) {
	forms := []string{
		`{"path":"a.go","old_string":"x","new_string":"y"}`,
		`{"file_path":"a.go","old_string":"x","new_string":"y"}`,
		`{"filePath":"a.go","old_string":"x","new_string":"y"}`,
	}
	var canonical string
	for i, in := range forms {
		got := extractToolKey(ToolCallRequest{Name: "edit", Input: json.RawMessage(in)}, "/repo")
		if got == "" {
			t.Fatalf("form %d produced no key: the alias is invisible to repeat tracking", i)
		}
		if i == 0 {
			canonical = got
			continue
		}
		if got != canonical {
			t.Errorf("form %d key %q != canonical key %q", i, got, canonical)
		}
	}
}

// A registered tool that declares nothing (an MCP wrapper) must run ALONE,
// exactly like an unlisted one. The batch executes concurrently and the file
// lock covers only edit/write, so batching a tool whose semantics are unknown is
// the very hazard the declarations exist to remove.
func TestSplitCallsByExecution_UnclassifiableRunsAlone(t *testing.T) {
	wrapped := map[string]ModelTool{
		"fs_write": {Type: "function", Function: ModelToolFunction{Name: "fs_write"}},
	}
	batch, sequential := splitCallsByExecution([]ToolCallRequest{{ID: "1", Name: "fs_write"}}, wrapped)
	if len(batch) != 0 {
		t.Errorf("unclassifiable tool must not be batched, got batch=%v", toolCallNames(batch))
	}
	if len(sequential) != 1 {
		t.Fatalf("unclassifiable tool must run alone, got sequential=%v", toolCallNames(sequential))
	}
}

// Scheduling and progress must agree on what "unclassifiable" means: a tool that
// is trusted as progress while being batched (or vice versa) is how the two
// halves of the same rule drift apart. This pins the shared predicate.
