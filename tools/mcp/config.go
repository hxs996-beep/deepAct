package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ServerConfig defines an MCP server to connect to.
type ServerConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// ConfigFile represents the .deepact/mcp.json configuration.
type ConfigFile struct {
	Servers []ServerConfig `json:"servers"`
}

// LoadConfig loads MCP server configurations from the given directory.
// It looks for .deepact/mcp.json relative to workDir, falling back to
// ~/.deepact/mcp.json if not found.
// If the project-level file exists but is invalid, the error is returned.
func LoadConfig(workDir string) (*ConfigFile, error) {
	// Project-level config takes priority
	if workDir != "" {
		cfg, err := loadFrom(filepath.Join(workDir, ".deepact", "mcp.json"))
		if err != nil {
			return nil, err // file exists but is invalid — report error
		}
		if cfg != nil {
			return cfg, nil
		}
	}
	// Fall back to user-level config
	home, err := os.UserHomeDir()
	if err == nil {
		cfg, err := loadFrom(filepath.Join(home, ".deepact", "mcp.json"))
		if err != nil {
			return nil, err
		}
		if cfg != nil {
			return cfg, nil
		}
	}
	return nil, nil
}

func loadFrom(path string) (*ConfigFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg ConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// Validate
	for i, s := range cfg.Servers {
		if s.Name == "" {
			return nil, fmt.Errorf("%s: server %d has empty name", path, i)
		}
		if s.Command == "" {
			return nil, fmt.Errorf("%s: server %q has empty command", path, s.Name)
		}
	}
	return &cfg, nil
}
