package skill

import (
	"fmt"
	"os"
	"path/filepath"
)

// SkillFile represents the parsed content of a SKILL.md file.
// All fields are populated by ParseMarkdownSkill from YAML frontmatter.
type SkillFile struct {
	Name                  string
	Description           string
	Keywords              []string
	Content               string
	NextSkills            []string
	AutoActivateThreshold *int

	// Claude Code-compatible fields
	AllowedTools           []string
	ArgumentHint           string
	Arguments              []string
	WhenToUse              string
	Model                  string
	Effort                 string
	Agent                  string
	Context                string
	Hooks                  string
	Paths                  []string
	UserInvocable          *bool // nil = default true
	DisableModelInvocation bool
	Version                string
	Shell                  string
	BaseDir                string
}

// LoadExternalSkills loads skill definitions from the given directory.
// Only the directory format is supported: <name>/SKILL.md (Claude Code layout).
//
// Returns nil, nil if the directory doesn't exist.
func LoadExternalSkills(dir string) ([]*Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read skills dir %s: %w", dir, err)
	}

	var skills []*Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue // only <name>/SKILL.md directory format is supported
		}
		// The references/ directory holds reference files namespaced
		// by skill name (references/<skill-name>/), not skill definitions.
		if entry.Name() == "references" {
			continue
		}
		skillMD := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillMD)
		if err != nil {
			continue // subdirectory has no SKILL.md, skip
		}
		sf, err := ParseMarkdownSkill(data)
		if err != nil {
			continue // skip unparseable skill files
		}
		if sf.Name == "" {
			sf.Name = entry.Name() // fallback to directory name
		}
		sf.BaseDir = filepath.Join(dir, entry.Name())
		skills = append(skills, SkillFromSkillFile(sf))
	}
	return skills, nil
}

// SkillFromSkillFile converts a parsed SkillFile into a Skill, applying
// default gate config (from gates.go) and default UserInvocable=true.
func SkillFromSkillFile(sf SkillFile) *Skill {
	s := &Skill{
		Name:                   sf.Name,
		Description:            sf.Description,
		Keywords:               sf.Keywords,
		Content:                sf.Content,
		NextSkills:             sf.NextSkills,
		AutoActivateThreshold:  sf.AutoActivateThreshold,
		AllowedTools:           sf.AllowedTools,
		ArgumentHint:           sf.ArgumentHint,
		Arguments:              sf.Arguments,
		WhenToUse:              sf.WhenToUse,
		Model:                  sf.Model,
		Effort:                 sf.Effort,
		Agent:                  sf.Agent,
		Context:                sf.Context,
		Hooks:                  sf.Hooks,
		Paths:                  sf.Paths,
		DisableModelInvocation: sf.DisableModelInvocation,
		Version:                sf.Version,
		Shell:                  sf.Shell,
		BaseDir:                sf.BaseDir,
	}
	if sf.UserInvocable != nil {
		s.UserInvocable = *sf.UserInvocable
	} else {
		s.UserInvocable = true
	}
	s.Gate = DefaultGateFor(sf.Name)
	return s
}

// LoadExternalSkillsFromPaths loads skills from multiple directories in order.
// Later directories override earlier ones if names conflict.
func LoadExternalSkillsFromPaths(dirs ...string) ([]*Skill, error) {
	seen := make(map[string]int)
	var skills []*Skill
	for _, dir := range dirs {
		loaded, err := LoadExternalSkills(dir)
		if err != nil {
			return nil, err
		}
		for _, s := range loaded {
			if idx, ok := seen[s.Name]; ok {
				skills[idx] = s // override
			} else {
				seen[s.Name] = len(skills)
				skills = append(skills, s)
			}
		}
	}
	return skills, nil
}
