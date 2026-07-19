package ui

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
	dlog "github.com/deepact/deepact/internal/log"
)

var runnerLog = dlog.New("[runner] ")

type EngineRunner interface {
	Run(prompt string) tea.Cmd
	Cancel()
	SetProgressChan(ch chan ProgressMsg)
	ValidateConnection() error
}

type DefaultEngineRunner struct {
	Eng        *engine.Engine
	progressCh chan ProgressMsg

	mu     sync.Mutex
	cancel context.CancelFunc
}

func (r *DefaultEngineRunner) SetProgressChan(ch chan ProgressMsg) {
	r.progressCh = ch
}

func (r *DefaultEngineRunner) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

func (r *DefaultEngineRunner) ValidateConnection() error {
	// DefaultEngineRunner is used in testing contexts where validation is not needed.
	return nil
}

func (r *DefaultEngineRunner) Run(prompt string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		r.mu.Lock()
		r.cancel = cancel
		r.mu.Unlock()
		defer cancel()

		resp, err := r.Eng.Run(ctx, prompt)
		return EngineResponseMsg{Response: resp, Err: err}
	}
}

type ProgressEngineRunner struct {
	Config     engine.EngineConfig
	Deps       engine.EngineDeps
	progressCh chan ProgressMsg

	once   sync.Once
	eng    *engine.Engine
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (r *ProgressEngineRunner) SetProgressChan(ch chan ProgressMsg) {
	r.progressCh = ch
}

func (r *ProgressEngineRunner) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

func (r *ProgressEngineRunner) getEngine() *engine.Engine {
	r.once.Do(func() {
		r.eng = engine.NewEngine(r.Config, r.Deps)
		r.eng.SetStopHooks([]engine.StopHook{
			&engine.ZeroToolCallHook{MaxRetries: 5},
			&engine.StalledNarrationHook{MaxRetries: 4, Classifier: r.eng.NewConclusionClassifier()},
		})
		r.eng.SetIntentJudge(r.eng.NewIntentClassifier())
	})
	return r.eng
}

func (r *ProgressEngineRunner) ValidateConnection() error {
	// Use a tiny completion call to verify the API key works.
	// Empty prompt with max_tokens=1 — cheapest possible validation.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := engine.ModelRequest{
		Model:     r.Config.ModelName,
		Messages:  []engine.ModelMessage{{Role: "user", Content: "ok"}},
		MaxTokens: 1,
	}
	_, err := r.Deps.Model.Complete(ctx, req)
	if err != nil {
		return fmt.Errorf("API key validation failed: %w", err)
	}
	return nil
}

func (r *ProgressEngineRunner) Run(prompt string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		r.mu.Lock()
		r.cancel = cancel
		r.mu.Unlock()
		defer cancel()

		eng := r.getEngine()
		eng.SetOnProgress(func(event engine.ProgressEvent) {
			if r.progressCh != nil {
				msg := ProgressMsg{Type: event.Type, Name: event.Name, Detail: event.Detail, FullDetail: event.FullDetail}
				if event.Usage != nil {
					msg.TokensIn = event.Usage.PromptTokens
					msg.TokensOut = event.Usage.CompletionTokens
					msg.CacheHit = event.Usage.CacheHitTokens
					msg.ModelName = event.ModelName
				}
				select {
				case r.progressCh <- msg:
				case <-time.After(100 * time.Millisecond):
				}
			}
		})
		resp, err := eng.Run(ctx, prompt)
		if err != nil {
			runnerLog.Printf("Engine.Run err: %v", err)
		}
		return EngineResponseMsg{Response: resp, Err: err}
	}
}
