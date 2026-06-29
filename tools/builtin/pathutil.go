package builtin

import (
	"errors"
	"fmt"
	"path/filepath"
)

// applyFilePathAlias returns the path a file-oriented tool should act on when
// the model may have used either the declared `path` key or the common
// `file_path` alias (Anthropic convention, which DeepSeek models sometimes
// emit). If primary is non-empty it wins; otherwise alias is used. Both are
// expected to already be trimmed by the caller when relevant.
//
// Rationale: the engine's path extraction (extractPathFromArgs) already accepts
// both keys, but the tool implementations declared only `path`. Without this
// alias the tool errors with "path is required" and the model retries in a
// loop. Accepting the alias at the tool layer closes that gap at the source.
func applyFilePathAlias(primary, alias string) string {
	if primary != "" {
		return primary
	}
	return alias
}

// resolveSafePath resolves targetPath relative to workDir to an absolute path.
//
// The agent is intentionally allowed to operate on ANY path the user/model
// supplies — this is an AI coding agent whose whole purpose is to read/edit
// files across multiple projects and directories, not just the launch
// directory. Workspace-range confinement made legitimate multi-project edits
// fail with "escapes workspace" (e.g. editing files under a sibling project
// the agent was asked to work on), which silently broke edit/write/glob/grep
// and left the Done history without a diff.
//
// Dangerous operations are intercepted by the Guards layer (ScopeGuard /
// risk thresholds), not by folder-range confinement here. Only the most
// destructive absolute paths are rejected as a backstop.
func resolveSafePath(workDir, targetPath string) (string, error) {
	if targetPath == "" {
		return "", errors.New("path is required")
	}

	var absTarget string
	if filepath.IsAbs(targetPath) {
		absTarget = filepath.Clean(targetPath)
	} else {
		if workDir == "" {
			return "", errors.New("work directory not configured")
		}
		absWorkDir, err := filepath.Abs(workDir)
		if err != nil {
			return "", fmt.Errorf("resolve work directory: %w", err)
		}
		absTarget = filepath.Join(absWorkDir, targetPath)
	}

	// Backstop: refuse a handful of catastrophic system paths. The Guards
	// layer handles risk-threshold-based interception for everything else.
	if isCatastrophicPath(absTarget) {
		return "", fmt.Errorf("refusing to operate on system path %q", absTarget)
	}

	return absTarget, nil
}

// isCatastrophicPath returns true for paths where a write/delete would be
// unrecoverable at the OS level. Deliberately tiny — the Guards layer is the
// real safety net; this is just a backstop against trivial mistakes.
func isCatastrophicPath(p string) bool {
	if p == "" {
		return false
	}
	// Resolve symlinks/.. so "/etc/../etc" is still caught.
	clean := filepath.Clean(p)
	switch clean {
	case "/", "/etc", "/usr", "/bin", "/sbin", "/var", "/System", "/Library":
		return true
	}
	// Windows drive roots.
	if len(clean) == 3 && clean[1] == ':' && (clean[2] == '/' || clean[2] == '\\') {
		return true
	}
	return false
}
