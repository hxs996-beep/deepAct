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

// A declared Mutating tool counts as progress when its call succeeded — this is
// the class that used to be missed unless the engine had the name in its list.
func TestMadeProgressFromCalls_Mutates(t *testing.T) {
	e := &Engine{config: EngineConfig{WorkDir: "/repo"}}
	specs := testSpecIndex()

	ok := map[string]string{"1": "ok"}
	if !e.madeProgressFromCalls([]ToolCallRequest{{ID: "1", Name: "rename_file"}}, ok, withExtra(specs, "rename_file", true, false)) {
		t.Error("a successful declared-mutating call must count as progress")
	}
	// A failed modification is not progress (nothing changed).
	failed := map[string]string{"1": "error"}
	if e.madeProgressFromCalls([]ToolCallRequest{{ID: "1", Name: "edit"}}, failed, specs) {
		t.Error("a failed mutation must not count as progress")
	}
}

// A declared information tool counts once per distinct call: re-reading the same
// artifact is not progress, which is what keeps a spinning run detectable.
func TestMadeProgressFromCalls_NovelInfoOnlyOnce(t *testing.T) {
	e := &Engine{config: EngineConfig{WorkDir: "/repo"}}
	specs := testSpecIndex()
	call := ToolCallRequest{ID: "1", Name: "artifact", Input: json.RawMessage(`{"ref":"sha256:abc"}`)}
	status := map[string]string{"1": "ok"}

	if !e.madeProgressFromCalls([]ToolCallRequest{call}, status, specs) {
		t.Fatal("the first read of an artifact must count as progress")
	}
	call.ID = "2"
	status = map[string]string{"2": "ok"}
	if e.madeProgressFromCalls([]ToolCallRequest{call}, status, specs) {
		t.Error("re-reading the same artifact must not count as progress")
	}
	// A different artifact is new information again.
	call.ID = "3"
	call.Input = json.RawMessage(`{"ref":"sha256:def"}`)
	if !e.madeProgressFromCalls([]ToolCallRequest{call}, map[string]string{"3": "ok"}, specs) {
		t.Error("a different artifact must count as progress")
	}
}

// An information tool with no known key shape still gets novelty tracking from
// its canonical arguments, so a new tool needs no per-tool engine code.
func TestMadeProgressFromCalls_NovelInfoGenericKey(t *testing.T) {
	e := &Engine{config: EngineConfig{WorkDir: "/repo"}}
	specs := withExtra(testSpecIndex(), "codegraph", false, true)
	call := ToolCallRequest{ID: "1", Name: "codegraph", Input: json.RawMessage(`{"q":"b","path":"/x"}`)}

	if !e.madeProgressFromCalls([]ToolCallRequest{call}, map[string]string{"1": "ok"}, specs) {
		t.Fatal("a novel call to a declared information tool must count as progress")
	}
	// Same arguments, different key order: the canonical form must dedupe it.
	call.ID = "2"
	call.Input = json.RawMessage(`{"path":"/x","q":"b"}`)
	if e.madeProgressFromCalls([]ToolCallRequest{call}, map[string]string{"2": "ok"}, specs) {
		t.Error("the same call with reordered arguments must not count again")
	}
}

// A tool the engine cannot classify (absent from the spec list, or listed
// without any declared semantic — e.g. an MCP wrapper) must be TRUSTED: a false
// no-progress block ends the user's run, which is worse than missing a loop.
func TestMadeProgressFromCalls_UnclassifiableIsTrusted(t *testing.T) {
	e := &Engine{config: EngineConfig{WorkDir: "/repo"}}

	undeclared := ToolCallRequest{ID: "1", Name: "some_mcp_tool", Input: json.RawMessage(`{}`)}
	if !e.madeProgressFromCalls([]ToolCallRequest{undeclared}, map[string]string{"1": "ok"}, nil) {
		t.Error("a tool absent from the spec list must be trusted as progress")
	}

	// Registered but declaring nothing at all (no Mutates, no NovelInfo).
	noMeta := map[string]ModelTool{
		"wrapped_mcp": {Type: "function", Function: ModelToolFunction{Name: "wrapped_mcp"}},
	}
	call := ToolCallRequest{ID: "2", Name: "wrapped_mcp", Input: json.RawMessage(`{}`)}
	if !e.madeProgressFromCalls([]ToolCallRequest{call}, map[string]string{"2": "ok"}, noMeta) {
		t.Error("a tool that declares no semantic must be trusted as progress")
	}
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
	e := &Engine{tools: stubToolExecutor{}, isChinese: true}
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
