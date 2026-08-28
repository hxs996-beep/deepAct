package ui

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
	dlog "github.com/deepact/deepact/internal/log"
	"github.com/deepact/deepact/session"
)

var runnerLog = dlog.New("[runner] ")

// SessionSummary 是 /resume 选择器展示的会话摘要（ui 自有类型，
// 避免 ui 直接依赖 session 包）。
type SessionSummary struct {
	ID         string
	UpdatedAt  time.Time
	FirstMsg   string
	EventCount int
}

type EngineRunner interface {
	Run(prompt string) tea.Cmd
	Cancel()
	SetProgressChan(ch chan ProgressMsg)
	ValidateConnection() error
	Steer(msg string)
	// ---- 会话恢复（/resume）----
	SetSessionID(id string)
	SetHistory(messages []engine.Message)
	ListSessions() []SessionSummary
	LoadHistory(id string) []engine.Message
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

func (r *DefaultEngineRunner) Steer(msg string) {
	r.Eng.Steer(msg)
}

func (r *DefaultEngineRunner) SetSessionID(id string)                 { _ = id }
func (r *DefaultEngineRunner) SetHistory(messages []engine.Message)   { _ = messages }
func (r *DefaultEngineRunner) ListSessions() []SessionSummary         { return nil }
func (r *DefaultEngineRunner) LoadHistory(id string) []engine.Message { return nil }

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
		// 无内建 stop hook：纯文本即结束（dsh 化）。StopHook 框架保留供未来扩展。
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

func (r *ProgressEngineRunner) Steer(msg string) {
	r.getEngine().Steer(msg)
}

func (r *ProgressEngineRunner) SetSessionID(id string) {
	r.getEngine().SetSessionID(id)
}

func (r *ProgressEngineRunner) SetHistory(messages []engine.Message) {
	r.getEngine().SetHistory(messages)
}

// ListSessions 返回当前 store 中的会话列表（经 deps.Session 类型断言访问
// *session.Store，store 在 cmd/run.go 注入）。
func (r *ProgressEngineRunner) ListSessions() []SessionSummary {
	store, ok := r.Deps.Session.(*session.Store)
	if !ok || store == nil {
		return nil
	}
	infos, err := store.List()
	if err != nil {
		return nil
	}
	out := make([]SessionSummary, 0, len(infos))
	for _, info := range infos {
		out = append(out, SessionSummary{
			ID:         info.ID,
			UpdatedAt:  info.UpdatedAt,
			FirstMsg:   info.FirstMsg,
			EventCount: info.EventCount,
		})
	}
	return out
}

// LoadHistory 读取会话事件并重建为可重放历史（裁剪+剥离工具链）。
func (r *ProgressEngineRunner) LoadHistory(id string) []engine.Message {
	events, err := r.Deps.Session.LoadEvents(id)
	if err != nil {
		return nil
	}
	return engine.RebuildHistory(events, engine.DefaultResumeBudget)
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
				msg := ProgressMsg{Type: event.Type, Name: event.Name, Detail: event.Detail, FullDetail: event.FullDetail, Todos: event.Todos}
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
