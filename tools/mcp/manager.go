package mcp

import (
	"fmt"
	"os"
	"os/exec"
)

// ManagedServer holds the state for a running MCP server connection.
type ManagedServer struct {
	Config ServerConfig
	Client *Client
	cmd    *exec.Cmd
	tools  []Tool
}

// StartServer starts an MCP server as a child process, performs the
// initialize handshake, and discovers its tools.
func StartServer(cfg ServerConfig) (*ManagedServer, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	// Inherit parent environment, then apply MCP-specific env vars
	cmd.Env = os.Environ()
	if cfg.Env != nil {
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr // forward stderr for debugging

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("start server %q: %w", cfg.Name, err)
	}

	transport := NewStdioTransport(stdin, stdout)
	client := NewClient(transport)

	_, err = client.Initialize()
	if err != nil {
		cmd.Process.Kill()
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("initialize %q: %w", cfg.Name, err)
	}

	tools, err := client.ListTools()
	if err != nil {
		cmd.Process.Kill()
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("list tools %q: %w", cfg.Name, err)
	}

	return &ManagedServer{
		Config: cfg,
		Client: client,
		cmd:    cmd,
		tools:  tools,
	}, nil
}

// Tools returns the tools discovered from this MCP server.
func (m *ManagedServer) Tools() []Tool {
	return m.tools
}

// Close terminates the MCP server process.
func (m *ManagedServer) Close() error {
	if m.cmd != nil && m.cmd.Process != nil {
		m.cmd.Process.Kill()
		m.cmd.Wait()
	}
	m.Client.Close()
	return nil
}
