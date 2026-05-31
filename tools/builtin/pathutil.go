package builtin

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

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
