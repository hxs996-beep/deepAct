package engine

import (
	"strings"
	"testing"
)

func TestAppendModifiedFilesSummary_Empty(t *testing.T) {
	if got := appendModifiedFilesSummary("done", nil, true); got != "done" {
		t.Errorf("no files should leave summary unchanged, got %q", got)
	}
}

func TestAppendModifiedFilesSummary_AppendsList(t *testing.T) {
	files := []string{"a.go", "b.go"}
	zh := appendModifiedFilesSummary("fixed it", files, true)
	if !strings.Contains(zh, "修改文件（2 个）：") || !strings.Contains(zh, "- a.go") || !strings.Contains(zh, "- b.go") {
		t.Errorf("zh summary missing modified files:\n%s", zh)
	}
	en := appendModifiedFilesSummary("", files, false)
	if !strings.Contains(en, "Files modified (2):") || !strings.Contains(en, "- b.go") {
		t.Errorf("en summary missing modified files:\n%s", en)
	}
	if !strings.HasPrefix(en, "Files modified") {
		t.Errorf("empty summary should lead with the files block:\n%s", en)
	}
}
