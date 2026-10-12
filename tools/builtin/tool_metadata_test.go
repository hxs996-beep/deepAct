package builtin

import (
	"testing"

	"github.com/deepact/deepact/tools"
)

// TestBuiltinToolMetadata pins every built-in tool's declared semantics. The
// engine keys scheduling (Sequential) and progress (Mutates / NovelInfo) off
// these declarations instead of a tool-name list, so a missing or wrong
// declaration is a functional regression: it is exactly what made artifact,
// skill_install and remember_preference invisible to the progress guard.
func TestBuiltinToolMetadata(t *testing.T) {
	cases := []struct {
		name       string
		spec       tools.ToolSpec
		mutates    bool
		novelInfo  bool
		sequential bool
	}{
		// Information-returning (read-only) tools.
		{"read", NewReadTool().Spec(), false, true, false},
		{"read_multi", NewReadMultiTool().Spec(), false, true, false},
		{"grep", NewGrepTool().Spec(), false, true, false},
		{"glob", NewGlobTool().Spec(), false, true, false},
		{"lsp", NewLSPToolWithOverrides(nil).Spec(), false, true, false},
		{"web_search", NewWebSearchTool(WebSearchConfig{}).Spec(), false, true, false},
		{"fetch", NewFetchTool().Spec(), false, true, false},
		{"artifact", NewArtifactTool().Spec(), false, true, false},

		// Modification tools. Only diff-rendered ones run sequentially.
		{"edit", NewEditTool().Spec(), true, false, true},
		{"write", NewWriteTool().Spec(), true, false, true},
		{"apply_patch", NewApplyPatchTool().Spec(), true, false, true},
		{"revert", NewRevertTool().Spec(), true, false, false},
		{"bash", NewBashTool().Spec(), true, false, false},
		{"skill_install", NewSkillInstallTool("", nil).Spec(), true, false, false},
		{"remember_preference", NewRememberPreferenceTool().Spec(), true, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.spec.Name != tc.name {
				t.Fatalf("spec name = %q, want %q", tc.spec.Name, tc.name)
			}
			if tc.spec.Mutates != tc.mutates {
				t.Errorf("Mutates = %v, want %v", tc.spec.Mutates, tc.mutates)
			}
			if tc.spec.NovelInfo != tc.novelInfo {
				t.Errorf("NovelInfo = %v, want %v", tc.spec.NovelInfo, tc.novelInfo)
			}
			if tc.spec.Sequential != tc.sequential {
				t.Errorf("Sequential = %v, want %v", tc.spec.Sequential, tc.sequential)
			}
			// Every built-in must declare at least one semantic, or the engine
			// cannot tell what it does and would have to fall back to trusting it.
			if !tc.spec.Mutates && !tc.spec.NovelInfo {
				t.Error("tool declares neither Mutates nor NovelInfo: the engine cannot classify it")
			}
		})
	}
}
