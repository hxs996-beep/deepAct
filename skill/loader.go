package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// SkillFile represents the TOML structure for an external skill file.
type SkillFile struct {
	Name                   string   `toml:"name"`
	Description            string   `toml:"description"`
	Keywords               []string `toml:"keywords"`
	Content                string   `toml:"content"`
	NextSkills             []string `toml:"next_skills"`
	AutoActivateThreshold  *int     `toml:"auto_activate_threshold"`
}

// LoadExternalSkills loads skill definitions from TOML files in the given
// directory. Each .toml file should contain a single skill definition.
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
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".toml") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read skill file %s: %w", path, err)
		}
		var sf SkillFile
		if err := toml.Unmarshal(data, &sf); err != nil {
			return nil, fmt.Errorf("parse skill file %s: %w", path, err)
		}
		if sf.Name == "" {
			continue
		}
		skills = append(skills, &Skill{
			Name:                  sf.Name,
			Description:           sf.Description,
			Keywords:              sf.Keywords,
			Content:               sf.Content,
			NextSkills:            sf.NextSkills,
			AutoActivateThreshold: sf.AutoActivateThreshold,
		})
	}
	return skills, nil
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
