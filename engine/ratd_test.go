package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCodePayload_Valid(t *testing.T) {
	lang, files, notes, err := parseCodePayload(`{"language":"go","source_files":[{"path":"src/q.go","content":"package q"}],"design_notes":"CAS queue"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lang != "go" || len(files) != 1 || files[0].Path != "src/q.go" || notes != "CAS queue" {
		t.Errorf("got lang=%q files=%v notes=%q", lang, files, notes)
	}
}

func TestParseCodePayload_NoFilesError(t *testing.T) {
	if _, _, _, err := parseCodePayload(`{"language":"go","source_files":[]}`); err == nil {
		t.Error("expected error for empty source_files")
	}
}

func TestParseCodePayload_InvalidJSON(t *testing.T) {
	if _, _, _, err := parseCodePayload(`not json`); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseTestPayload_NoIssues(t *testing.T) {
	_, noIssues, err := parseTestPayload(`{"no_issues_found": true}`)
	if err != nil || !noIssues {
		t.Fatalf("expected no_issues=true, got noIssues=%v err=%v", noIssues, err)
	}
}

func TestParseTestPayload_Valid(t *testing.T) {
	test, noIssues, err := parseTestPayload(`{"test_category":"CONCURRENCY_STRESS","severity":"P0_CRITICAL","target_file":"src/q_test.go","test_code":"func TestX(t *testing.T){}","assertion_rationale":"deadlock"}`)
	if err != nil || noIssues {
		t.Fatalf("unexpected: noIssues=%v err=%v", noIssues, err)
	}
	if test == nil || test.Severity != "P0_CRITICAL" || test.TargetFile != "src/q_test.go" {
		t.Errorf("test = %+v", test)
	}
}

func TestParseTestPayload_MissingFields(t *testing.T) {
	if _, _, err := parseTestPayload(`{"severity":"P1_HIGH"}`); err == nil {
		t.Error("expected error for missing test_code/target_file")
	}
}

func TestParseArbitration_Accept(t *testing.T) {
	decision, _, err := parseArbitration(`{"decision":"ACCEPT_TEST","rejected_reason":"","actionable_feedback":"fix leak"}`)
	if err != nil || decision != "ACCEPT_TEST" {
		t.Fatalf("got decision=%q err=%v", decision, err)
	}
}

func TestParseArbitration_InvalidDecision(t *testing.T) {
	if _, _, err := parseArbitration(`{"decision":"MAYBE"}`); err == nil {
		t.Error("expected error for invalid decision")
	}
}

func TestWriteSourceFiles_WritesUnderWorkdir(t *testing.T) {
	dir := t.TempDir()
	files := []RATDSourceFile{{Path: "src/q.go", Content: "package q"}}
	if err := writeSourceFiles(files, dir); err != nil {
		t.Fatalf("writeSourceFiles: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "src", "q.go"))
	if err != nil || string(b) != "package q" {
		t.Errorf("read back = %q, err=%v", b, err)
	}
}

func TestWriteSourceFiles_RejectsEscape(t *testing.T) {
	dir := t.TempDir()
	files := []RATDSourceFile{{Path: "../evil.go", Content: "x"}}
	if err := writeSourceFiles(files, dir); err == nil {
		t.Error("expected error for path escaping workdir")
	}
}

func TestWriteSourceFiles_RejectsAbsolute(t *testing.T) {
	dir := t.TempDir()
	files := []RATDSourceFile{{Path: "/etc/evil.go", Content: "x"}}
	if err := writeSourceFiles(files, dir); err == nil {
		t.Error("expected error for absolute path")
	}
}

func TestWriteSourceFiles_RejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	files := []RATDSourceFile{{Path: "link/evil.go", Content: "x"}}
	if err := writeSourceFiles(files, dir); err == nil {
		t.Fatal("expected error for symlink escape")
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.go")); !os.IsNotExist(err) {
		t.Errorf("evil.go must not be written outside workdir, stat err=%v", err)
	}
}

func TestParseTestPayload_AmbiguousNoIssuesAndTest(t *testing.T) {
	_, _, err := parseTestPayload(`{"no_issues_found": true, "test_code":"x", "target_file":"y"}`)
	if err == nil {
		t.Error("expected error for no_issues_found=true with test fields")
	}
}

func TestParseTestPayload_NoIssuesFalse(t *testing.T) {
	test, noIssues, err := parseTestPayload(`{"no_issues_found": false, "test_category":"CORRECTNESS","severity":"P1_HIGH","target_file":"a_test.go","test_code":"func T(){}","assertion_rationale":"r"}`)
	if err != nil || noIssues {
		t.Fatalf("unexpected: noIssues=%v err=%v", noIssues, err)
	}
	if test == nil || test.TargetFile != "a_test.go" || test.TestCode != "func T(){}" {
		t.Errorf("test = %+v", test)
	}
}

func TestWriteSourceFiles_EmptyPath(t *testing.T) {
	dir := t.TempDir()
	files := []RATDSourceFile{{Path: "", Content: "x"}}
	if err := writeSourceFiles(files, dir); err == nil {
		t.Error("expected error for empty path")
	}
}
