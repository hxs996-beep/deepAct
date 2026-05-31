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
}

type StatusUpdateMsg struct {
	Info StatusInfo
}

type ApiKeySetMsg struct {
	Key string
}
