package engine

import (
	"fmt"
	"sync"
)

// AgentRegistry manages available sub-agents.
type AgentRegistry struct {
	mu     sync.RWMutex
	agents map[AgentID]Agent
}

// NewAgentRegistry creates an empty agent registry.
func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{
		agents: make(map[AgentID]Agent),
	}
}

// Register adds an agent to the registry.
func (r *AgentRegistry) Register(a Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[a.ID()] = a
}

// Get retrieves an agent by ID.
func (r *AgentRegistry) Get(id AgentID) (Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[id]
	if !ok {
		return nil, fmt.Errorf("agent not found: %s", id)
	}
	return a, nil
}

// AgentSpecs returns descriptions of all registered agents.
func (r *AgentRegistry) AgentSpecs() []AgentSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	specs := make([]AgentSpec, 0, len(r.agents))
	for _, a := range r.agents {
		specs = append(specs, a.Spec())
	}
	return specs
}

// ForEach calls fn for each registered agent.
func (r *AgentRegistry) ForEach(fn func(Agent)) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, a := range r.agents {
		fn(a)
	}
}

// compile-time check: *AgentRegistry satisfies the interface added to EngineDeps
var _ interface {
	Get(AgentID) (Agent, error)
	Register(Agent)
} = (*AgentRegistry)(nil)
