package ui

import "github.com/deepact/deepact/engine"

type TickMsg struct{}

type StreamDeltaMsg struct {
	Content string
}

type ToolStartMsg struct {
	Name string
	Args string
}

type ToolDoneMsg struct {
	Name   string
	Digest string
}

type AgentStartMsg struct {
	Role string
	Goal string
}

type AgentDoneMsg struct {
	Role    string
	Summary string
}

type EngineResponseMsg struct {
	Response *engine.EngineResponse
	Err      error
	// RunSeq is the seq of the Run() call that produced this message,
	// assigned by the Model when it starts a run and passed through the
	// EngineRunner. The Model drops messages whose RunSeq doesn't match the
	// current run, so a cancelled run's response can never pop up
	// ("任务已取消。") after the user has already started a new run.
	RunSeq uint64
}

type StatusUpdateMsg struct {
	Info StatusInfo
}

type ApiKeySetMsg struct {
	Key string
}
