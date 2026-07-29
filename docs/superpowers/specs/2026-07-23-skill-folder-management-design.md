# Skill Folder Management Alignment with Claude Code

**Date**: 2026-07-23
**Status**: Approved

## Goal

Align DeepAct's skill folder management with Claude Code: directory-only format
(`<name>/SKILL.md`), remove TOML entirely, and add full support for all 14
Claude Code frontmatter fields.

## Background

DeepAct's skill loader supports both flat files (`.toml`/`.md`) and nested
directories (`<name>/SKILL.md`). Claude Code only supports the directory format.
Investigation found zero TOML skill files in the project or user installations--
TOML is a DeepAct invention never adopted in practice. All skills in the wild
(Claude Code, obra/superpowers, DeepAct's own `.claude/skills/`) use `SKILL.md`.

## Design

### 1. Remove TOML Entirely

- Remove `github.com/BurntSushi/toml` import from `skill/loader.go` and
  `tools/builtin/skill_install.go`
- Remove `parseSkillFile` function; call `ParseMarkdownSkill` directly
- `SkillFile` struct loses all `toml:` tags
- `SkillFile.Gate` field removed (gates provided solely by `gates.go` defaults)
- `skill_install` registry URL changes from `.toml` to `.md`
- `skill_install` removes TOML detection/parsing path

### 2. Directory-Only Format

`LoadExternalSkills` scans subdirectories for `SKILL.md` only:

```
~/.deepact/skills/
  brainstorming/
    SKILL.md          # only format supported
  references/          # skipped (reference files, not skills)
```

Flat `.toml`/`.md` files at top level are no longer loaded.

### 3. New Skill Struct Fields

Added to `Skill` and `SkillFile`:

| Field | Type | YAML key | Default |
|-------|------|----------|---------|
| AllowedTools | []string | allowed-tools | nil |
| ArgumentHint | string | argument-hint | "" |
| Arguments | []string | arguments | nil |
| WhenToUse | string | when_to_use | "" |
| Model | string | model | "" |
| Effort | string | effort | "" |
| Agent | string | agent | "" |
| Context | string | context | "" |
| Hooks | string | hooks | "" |
| Paths | []string | paths | nil |
| UserInvocable | bool | user-invocable | true |
| DisableModelInvocation | bool | disable-model-invocation | false |
| Version | string | version | "" |
| Shell | string | shell | "" |
| BaseDir | string | (set by loader) | "" |

### 4. Markdown Parser Enhancement

`ParseMarkdownSkill` frontmatter parser enhanced to:
- Parse all 14 new fields with Claude Code naming conventions (kebab-case for
  `allowed-tools`, `argument-hint`, `user-invocable`, `disable-model-invocation`;
  snake_case for `when_to_use`; lowercase for rest)
- Support multi-line YAML lists (`- item` syntax) for list fields
- Parse boolean values for `user-invocable` and `disable-model-invocation`

### 5. skill_install Directory Format

- Registry URL: `<registry>/<name>.md`
- Save path: `<skillsDir>/<name>/SKILL.md`
- Only markdown parsing (no TOML fallback)

### 6. buildSkillsBlock Update

- Skip skills with `DisableModelInvocation == true`
- Append `WhenToUse` to each skill's listing if non-empty

### 7. Gate Configuration

Gates are solely provided by `gates.go` `DefaultGateFor()`. No gate field in
skill files. This matches current practice--no skill file has ever included a
gate section.

## Files Changed

1. `skill/skill.go` - Add 15 fields to Skill struct
2. `skill/loader.go` - Remove TOML, directory-only loader, new SkillFile fields
3. `skill/markdown.go` - Enhanced frontmatter parser
4. `tools/builtin/skill_install.go` - MD-only, directory format save
5. `cmd/run.go` - buildSkillsBlock filter + when_to_use
6. `skill/loader_test.go` - Update tests for directory-only format
7. `skill/markdown_test.go` - Add new field parsing tests

## Breaking Changes

- Flat `.md`/`.toml` files in skill directories no longer loaded
- Must migrate to `<name>/SKILL.md` directory format
- Project's `.claude/skills/` (already directory format) is unaffected

## Testing

- Existing tests updated for directory-only format
- New tests for multi-line YAML list parsing
- New tests for boolean field parsing
- New tests for all new frontmatter fields
- `go build ./...` and `go test ./skill/... ./tools/builtin/...` must pass
