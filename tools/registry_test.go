package tools

import (
	"encoding/json"
	"sync"
	"testing"
)

type mockTool struct {
	name   string
	result ToolResultEnvelope
	err    error
	delay  func()
}

func (m *mockTool) Spec() ToolSpec {
	return ToolSpec{Name: m.name, Description: "mock", Parameters: json.RawMessage(`{}`)}
}

func (m *mockTool) Run(ctx ToolContext, input json.RawMessage) (ToolResultEnvelope, error) {
	if m.delay != nil {
		m.delay()
	}
	return m.result, m.err
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := NewRegistry()
	tool := &mockTool{name: "test_tool", result: ToolResultEnvelope{Status: StatusOK, Digest: "ok"}}
	reg.Register(tool)

	got, ok := reg.Get("test_tool")
	if !ok {
		t.Fatal("expected tool to be found")
	}
	if got.Spec().Name != "test_tool" {
		t.Errorf("name = %q, want %q", got.Spec().Name, "test_tool")
	}

	_, ok = reg.Get("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent tool")
	}
}

func TestRegistry_AllSpecs(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockTool{name: "a"})
	reg.Register(&mockTool{name: "b"})
	reg.Register(&mockTool{name: "c"})

	specs := reg.AllSpecs()
	if len(specs) != 3 {
		t.Errorf("specs count = %d, want 3", len(specs))
	}
}

// TestRegistry_AllSpecs_DeterministicOrder guards the prefix-cache contract:
// DeepSeek tokenizes the tools array before messages, so any per-turn ordering
// jitter (Go map iteration is deliberately randomized) breaks the cached prefix
// from the very first byte. The returned specs must be sorted by name regardless
// of registration order or map internals.
func TestRegistry_AllSpecs_DeterministicOrder(t *testing.T) {
	reg := NewRegistry()
	// Register in scrambled order — the old map-iteration implementation
	// returned these in random order, silently invalidating the cache prefix.
	reg.Register(&mockTool{name: "read"})
	reg.Register(&mockTool{name: "grep"})
	reg.Register(&mockTool{name: "bash"})
	reg.Register(&mockTool{name: "edit"})
	reg.Register(&mockTool{name: "write"})

	specs := reg.AllSpecs()
	if len(specs) != 5 {
		t.Fatalf("specs count = %d, want 5", len(specs))
	}
	for i := 1; i < len(specs); i++ {
		if specs[i-1].Name > specs[i].Name {
			t.Errorf("specs not sorted at index %d: %q > %q", i, specs[i-1].Name, specs[i].Name)
		}
	}

	// Calling twice must yield byte-identical order (cache stability).
	second := reg.AllSpecs()
	for i := range specs {
		if specs[i].Name != second[i].Name {
			t.Fatalf("AllSpecs order changed between calls: first=%q second=%q", specs[i].Name, second[i].Name)
		}
	}
}

func TestExecutor_SingleCall(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockTool{name: "echo", result: ToolResultEnvelope{Status: StatusOK, Digest: "hello"}})

	exec := NewExecutor(reg)
	results := exec.Execute(ToolContext{}, []ToolCall{
		{ID: "c1", Name: "echo", Input: json.RawMessage(`{}`)},
	})

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Status != StatusOK {
		t.Errorf("status = %q, want %q", results[0].Status, StatusOK)
	}
	if results[0].Digest != "hello" {
		t.Errorf("digest = %q, want %q", results[0].Digest, "hello")
	}
	if results[0].ToolCallID != "c1" {
		t.Errorf("tool_call_id = %q, want %q", results[0].ToolCallID, "c1")
	}
}

func TestExecutor_ParallelCalls(t *testing.T) {
	reg := NewRegistry()

	var mu sync.Mutex
	order := make([]string, 0)

	reg.Register(&mockTool{name: "a", result: ToolResultEnvelope{Digest: "result_a"}, delay: func() {
		mu.Lock()
		order = append(order, "a")
		mu.Unlock()
	}})
	reg.Register(&mockTool{name: "b", result: ToolResultEnvelope{Digest: "result_b"}, delay: func() {
		mu.Lock()
		order = append(order, "b")
		mu.Unlock()
	}})

	exec := NewExecutor(reg)
	results := exec.Execute(ToolContext{}, []ToolCall{
		{ID: "c1", Name: "a", Input: json.RawMessage(`{}`)},
		{ID: "c2", Name: "b", Input: json.RawMessage(`{}`)},
	})

	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].Digest != "result_a" {
		t.Errorf("results[0].digest = %q, want %q", results[0].Digest, "result_a")
	}
	if results[1].Digest != "result_b" {
		t.Errorf("results[1].digest = %q, want %q", results[1].Digest, "result_b")
	}
	mu.Lock()
	if len(order) != 2 {
		t.Errorf("both tools should have run, got %d", len(order))
	}
	mu.Unlock()
}

func TestExecutor_ToolNotFound(t *testing.T) {
	reg := NewRegistry()
	exec := NewExecutor(reg)

	results := exec.Execute(ToolContext{}, []ToolCall{
		{ID: "c1", Name: "missing", Input: json.RawMessage(`{}`)},
	})

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Status != StatusError {
		t.Errorf("status = %q, want %q", results[0].Status, StatusError)
	}
	if results[0].Digest == "" {
		t.Error("expected error digest for missing tool")
	}
}

func TestExecutor_EmptyCalls(t *testing.T) {
	reg := NewRegistry()
	exec := NewExecutor(reg)
	results := exec.Execute(ToolContext{}, nil)
	if len(results) != 0 {
		t.Errorf("results = %d, want 0", len(results))
	}
}
