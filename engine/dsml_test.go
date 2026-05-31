package engine

import (
	"testing"
)

func TestParseDSMLToolCalls_SingleInvoke(t *testing.T) {
	content := `需要先补读一些关键代码。
<｜｜DSML｜｜tool_calls><｜｜DSML｜｜invoke name="grep"><｜｜DSML｜｜parameter name="pattern" string="true">getMailSystem</｜｜DSML｜｜parameter><｜｜DSML｜｜parameter name="path" string="true">/path/to/file.java</｜｜DSML｜｜parameter></｜｜DSML｜｜invoke></｜｜DSML｜｜tool_calls>`

	cleaned, calls, found := parseDSMLToolCalls(content)
	if !found {
		t.Fatal("expected DSML tool calls to be found")
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Function.Name != "grep" {
		t.Errorf("expected tool name 'grep', got %q", calls[0].Function.Name)
	}
	if calls[0].Type != "function" {
		t.Errorf("expected type 'function', got %q", calls[0].Type)
	}
	if cleaned != "需要先补读一些关键代码。" {
		t.Errorf("cleaned content = %q", cleaned)
	}

	args := calls[0].Function.Arguments
	if args == "" {
		t.Fatal("arguments should not be empty")
	}
	if !contains(args, "getMailSystem") {
		t.Errorf("arguments should contain pattern value, got %s", args)
	}
	if !contains(args, "/path/to/file.java") {
		t.Errorf("arguments should contain path value, got %s", args)
	}
}

func TestParseDSMLToolCalls_MultipleInvokes(t *testing.T) {
	content := `<｜｜DSML｜｜tool_calls><｜｜DSML｜｜invoke name="grep"><｜｜DSML｜｜parameter name="pattern" string="true">foo</｜｜DSML｜｜parameter></｜｜DSML｜｜invoke><｜｜DSML｜｜invoke name="bash"><｜｜DSML｜｜parameter name="command" string="true">ls -la</｜｜DSML｜｜parameter></｜｜DSML｜｜invoke></｜｜DSML｜｜tool_calls>`

	_, calls, found := parseDSMLToolCalls(content)
	if !found {
		t.Fatal("expected DSML tool calls to be found")
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Function.Name != "grep" {
		t.Errorf("call[0] name = %q, want grep", calls[0].Function.Name)
	}
	if calls[1].Function.Name != "bash" {
		t.Errorf("call[1] name = %q, want bash", calls[1].Function.Name)
	}
}

func TestParseDSMLToolCalls_SingleBarVariant(t *testing.T) {
	content := `<｜DSML｜tool_calls><｜DSML｜invoke name="read"><｜DSML｜parameter name="path" string="true">/tmp/test.go</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`

	_, calls, found := parseDSMLToolCalls(content)
	if !found {
		t.Fatal("expected DSML tool calls to be found (single bar variant)")
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Function.Name != "read" {
		t.Errorf("expected tool name 'read', got %q", calls[0].Function.Name)
	}
}

func TestParseDSMLToolCalls_NoMatch(t *testing.T) {
	content := "This is normal content without any DSML tokens."
	cleaned, calls, found := parseDSMLToolCalls(content)
	if found {
		t.Error("should not find DSML tool calls in normal content")
	}
	if len(calls) != 0 {
		t.Errorf("calls should be empty, got %d", len(calls))
	}
	if cleaned != content {
		t.Errorf("cleaned should equal original content")
	}
}

func TestParseDSMLToolCalls_MultilineParams(t *testing.T) {
	content := `<｜｜DSML｜｜tool_calls><｜｜DSML｜｜invoke name="bash"><｜｜DSML｜｜parameter name="command" string="true">cd /workspace && grep -rn "pattern"
src/main/java/</｜｜DSML｜｜parameter></｜｜DSML｜｜invoke></｜｜DSML｜｜tool_calls>`

	_, calls, found := parseDSMLToolCalls(content)
	if !found {
		t.Fatal("expected DSML tool calls to be found")
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	args := calls[0].Function.Arguments
	if !contains(args, "cd /workspace") {
		t.Errorf("multiline command should be preserved, got %s", args)
	}
}

func TestParseDSMLToolCalls_LineWrappedPath(t *testing.T) {
	content := `<｜｜DSML｜｜tool_calls> <｜｜DSML｜｜invoke name="read">
<｜｜DSML｜｜parameter name="path"
string="true">/Users/admin/workspace/mailarchive/src/main/java/com/netease/mail/ar
chive/service/archtomail/impl/ArchToMailServiceImpl.
java</｜｜DSML｜｜parameter>
</｜｜DSML｜｜invoke> </｜｜DSML｜｜tool_calls>`

	_, calls, found := parseDSMLToolCalls(content)
	if !found {
		t.Fatal("expected DSML tool calls to be found")
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Function.Name != "read" {
		t.Errorf("expected tool name 'read', got %q", calls[0].Function.Name)
	}
	args := calls[0].Function.Arguments
	expectedPath := "/Users/admin/workspace/mailarchive/src/main/java/com/netease/mail/archive/service/archtomail/impl/ArchToMailServiceImpl.java"
	if !contains(args, expectedPath) {
		t.Errorf("line-wrapped path should be collapsed.\nwant path: %s\ngot args: %s", expectedPath, args)
	}
}

func TestHasDSMLToolCalls(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"normal text", false},
		{"<｜｜DSML｜｜tool_calls>something</｜｜DSML｜｜tool_calls>", true},
		{"<||DSML||tool_calls>something</||DSML||tool_calls>", true},
		{"mentions the markup language but no calls keyword", false},
		{"mentions function calls but no markup", false},
	}
	for _, tt := range tests {
		if got := hasDSMLToolCalls(tt.input); got != tt.want {
			t.Errorf("hasDSMLToolCalls(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestStripDSMLTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no DSML",
			input: "normal content",
			want:  "normal content",
		},
		{
			name:  "full-width complete",
			input: "before <｜｜DSML｜｜tool_calls><｜｜DSML｜｜invoke name=\"read\"><｜｜DSML｜｜parameter name=\"path\" string=\"true\">/tmp/x</｜｜DSML｜｜parameter></｜｜DSML｜｜invoke></｜｜DSML｜｜tool_calls> after",
			want:  "before  after",
		},
		{
			name:  "ASCII pipe complete",
			input: "text <||DSML||tool_calls><||DSML||invoke name=\"read\"><||DSML||parameter name=\"path\">/tmp</||DSML||parameter></||DSML||invoke></||DSML||tool_calls>",
			want:  "text",
		},
		{
			name:  "incomplete/truncated (no closing tag)",
			input: "思考中...\n<｜｜DSML｜｜tool_calls><｜｜DSML｜｜invoke name=\"grep\"><｜｜DSML｜｜parameter name=\"pattern\"",
			want:  "思考中...",
		},
		{
			name:  "empty after strip",
			input: "<｜｜DSML｜｜tool_calls><｜｜DSML｜｜invoke name=\"x\"></｜｜DSML｜｜invoke></｜｜DSML｜｜tool_calls>",
			want:  "",
		},
	}
	for _, tt := range tests {
		got := stripDSMLTokens(tt.input)
		if got != tt.want {
			t.Errorf("stripDSMLTokens[%s]:\n  got:  %q\n  want: %q", tt.name, got, tt.want)
		}
	}
}

func TestParseDSMLToolCalls_AsciiPipes(t *testing.T) {
	content := `<||DSML||tool_calls><||DSML||invoke name="read"><||DSML||parameter name="path" string="true">/tmp/test.go</||DSML||parameter></||DSML||invoke></||DSML||tool_calls>`

	_, calls, found := parseDSMLToolCalls(content)
	if !found {
		t.Fatal("expected DSML tool calls to be found (ASCII pipe variant)")
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Function.Name != "read" {
		t.Errorf("expected tool name 'read', got %q", calls[0].Function.Name)
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
