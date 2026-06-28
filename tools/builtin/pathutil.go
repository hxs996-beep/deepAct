package builtin

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

// resolveSafePath resolves targetPath relative to workDir and ensures it
// does not escape the workspace (prevents path traversal attacks).
// Returns the safe absolute path or an error.
func resolveSafePath(workDir, targetPath string) (string, error) {
	if workDir == "" {
		return "", errors.New("work directory not configured")
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve work directory: %w", err)
	}

	var absTarget string
	if filepath.IsAbs(targetPath) {
		absTarget = filepath.Clean(targetPath)
	} else {
		absTarget = filepath.Join(absWorkDir, targetPath)
	}

	// Ensure resolved path is within workdir
	workDirPrefix := absWorkDir + string(filepath.Separator)
	if absTarget != absWorkDir && !strings.HasPrefix(absTarget, workDirPrefix) {
		return "", fmt.Errorf("path %q escapes workspace (resolved to %q)", targetPath, absTarget)
	}

	return absTarget, nil
}
