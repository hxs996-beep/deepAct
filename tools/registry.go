package tools

import (
	"encoding/json"
	"fmt"
	"sync"
)

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolContext struct {
	WorkDir     string
	SessionID   string
	TurnNumber  int
	ArtifactDir string // base directory for artifact store (e.g., ~/.deepact/artifacts)
}

type ToolResultEnvelope struct {
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Status      string `json:"status"`
	Digest      string `json:"digest"`
	ArtifactRef string `json:"artifact_ref,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
}

const (
	StatusOK    = "ok"
	StatusError = "error"
)

type Tool interface {
	Spec() ToolSpec
	Run(ctx ToolContext, input json.RawMessage) (ToolResultEnvelope, error)
}

type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type Executor struct {
	registry  *Registry
	fileLocks map[string]*sync.Mutex
	lockMu    sync.Mutex
}

func NewExecutor(registry *Registry) *Executor {
	return &Executor{
		registry:  registry,
		fileLocks: make(map[string]*sync.Mutex),
	}
}

// fileLock returns a per-path mutex, creating one if needed.
func (e *Executor) fileLock(path string) *sync.Mutex {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	mu, ok := e.fileLocks[path]
	if !ok {
		mu = &sync.Mutex{}
		e.fileLocks[path] = mu
	}
	return mu
}

// extractPath extracts the "path" field from a tool input JSON.
func extractPath(input json.RawMessage) string {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return ""
	}
	return p.Path
}

func (e *Executor) Execute(ctx ToolContext, calls []ToolCall) []ToolResultEnvelope {
	results := make([]ToolResultEnvelope, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		index := i
		current := call
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Serialize Edit/Write calls to the same file to prevent write races.
			var mu *sync.Mutex
			if current.Name == "edit" || current.Name == "write" {
				if path := extractPath(current.Input); path != "" {
					mu = e.fileLock(path)
					mu.Lock()
				}
			}

			tool, ok := e.registry.Get(current.Name)
			if !ok {
				if mu != nil {
					mu.Unlock()
				}
				results[index] = ToolResultEnvelope{
					ToolCallID: current.ID,
					ToolName:   current.Name,
					Status:     StatusError,
					Digest:     fmt.Sprintf("tool not found: %s", current.Name),
				}
				return
			}

			env, err := tool.Run(ctx, current.Input)
			if mu != nil {
				mu.Unlock()
			}
			if env.ToolCallID == "" {
				env.ToolCallID = current.ID
			}
			if env.ToolName == "" {
				env.ToolName = current.Name
			}
			if env.Status == "" {
				if err != nil {
					env.Status = StatusError
				} else {
					env.Status = StatusOK
				}
			}
			if err != nil && env.Digest == "" {
				env.Digest = err.Error()
			}
			results[index] = env
		}()
	}
	if len(calls) > 0 {
		wg.Wait()
	}
	return results
}

type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Spec().Name] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) AllSpecs() []ToolSpec {
	specs := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		specs = append(specs, t.Spec())
	}
	return specs
}
