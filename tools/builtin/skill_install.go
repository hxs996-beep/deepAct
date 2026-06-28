package builtin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/deepact/deepact/skill"
	"github.com/deepact/deepact/tools"
)

// DefaultSkillRegistry is the URL base for fetching community skills.
// Skills are fetched from <registry>/<name>.toml
const DefaultSkillRegistry = "https://raw.githubusercontent.com/deepact/skills/main"

// SkillInstallTool allows the LLM to install skills from a registry.
type SkillInstallTool struct {
	skillsDir string // e.g., ~/.deepact/skills/
	registry  *skill.Registry
	client    *http.Client
}

func NewSkillInstallTool(skillsDir string, reg *skill.Registry) *SkillInstallTool {
	return &SkillInstallTool{
		skillsDir: skillsDir,
		registry:  reg,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (t *SkillInstallTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "skill_install",
		Description: "Install a skill from the community registry. Fetches the skill definition by name and saves it to ~/.deepact/skills/. After installation, the skill is immediately available. Optionally provide a custom source_url to install from a specific TOML file URL.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Skill name to install (e.g., 'brainstorming', 'debugging')"},"source_url":{"type":"string","description":"Optional: custom URL to a .toml skill file. If omitted, fetches from the default community registry."}},"required":["name"]}`),
	}
}

type skillInstallInput struct {
	Name      string `json:"name"`
	SourceURL string `json:"source_url"`
}

func (t *SkillInstallTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload skillInstallInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Name = strings.TrimSpace(payload.Name)
	if payload.Name == "" {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: "skill name is required"}, fmt.Errorf("skill name is required")
	}

	// Determine source URL
	sourceURL := payload.SourceURL
	if sourceURL == "" {
		sourceURL = fmt.Sprintf("%s/%s.toml", DefaultSkillRegistry, payload.Name)
	}

	// Fetch the .toml file
	resp, err := t.client.Get(sourceURL)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("fetch failed: %v", err)}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return tools.ToolResultEnvelope{
			Status: tools.StatusError,
			Digest: fmt.Sprintf("fetch failed: HTTP %d — skill '%s' not found at %s", resp.StatusCode, payload.Name, sourceURL),
		}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB max
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("read response: %v", err)}, err
	}

	// Validate TOML before saving
	var sf skill.SkillFile
	if err := toml.Unmarshal(body, &sf); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid skill file: %v", err)}, err
	}
	if sf.Name == "" {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: "skill file has no name field"}, fmt.Errorf("no name field")
	}

	// Ensure target directory exists
	if err := os.MkdirAll(t.skillsDir, 0755); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("create skills dir: %v", err)}, err
	}

	// Write to ~/.deepact/skills/<name>.toml
	targetPath := filepath.Join(t.skillsDir, payload.Name+".toml")
	if err := os.WriteFile(targetPath, body, 0644); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("write skill file: %v", err)}, err
	}

	// Register in the running registry (overrides embedded if name matches)
	t.registry.Register(&skill.Skill{
		Name:                  sf.Name,
		Description:           sf.Description,
		Keywords:              sf.Keywords,
		Content:               sf.Content,
		NextSkills:            sf.NextSkills,
		AutoActivateThreshold: sf.AutoActivateThreshold,
	})

	digest := fmt.Sprintf("✅ Skill '%s' installed to %s\n   Description: %s", sf.Name, targetPath, sf.Description)
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: digest}, nil
}

// compile-time check: SkillInstallTool implements Tool
var _ tools.Tool = (*SkillInstallTool)(nil)
