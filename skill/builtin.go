package skill

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
)

//go:embed builtin/*/SKILL.md
var builtinSkillFS embed.FS

// BuiltinSkills 解析嵌入的官方保留技能（ratd/collab）。
// 内置技能是最低优先级：cmd/run.go 先注册内置、后注册用户目录，同名覆盖。
func BuiltinSkills() ([]*Skill, error) {
	var skills []*Skill
	entries, err := fs.ReadDir(builtinSkillFS, "builtin")
	if err != nil {
		return nil, fmt.Errorf("read builtin skills dir: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		data, err := builtinSkillFS.ReadFile(path.Join("builtin", name, "SKILL.md"))
		if err != nil {
			return nil, fmt.Errorf("read builtin skill %s: %w", name, err)
		}
		sf, err := ParseMarkdownSkill(data)
		if err != nil {
			return nil, fmt.Errorf("parse builtin skill %s: %w", name, err)
		}
		if sf.Name == "" {
			sf.Name = name
		}
		sf.BaseDir = path.Join("builtin", name)
		skills = append(skills, SkillFromSkillFile(sf))
	}
	return skills, nil
}
