package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- CompressionOrchestrator ---

func TestNewCompressionOrchestrator(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "deepseek-v4-pro")
	if c == nil {
		t.Fatal("expected non-nil CompressionOrchestrator")
	}
	if c.flashModelName != "deepseek-v4-flash" {
		t.Errorf("flashModelName = %q, want deepseek-v4-flash", c.flashModelName)
	}
}

func TestSetFlashModelName(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	c.SetFlashModelName("custom-flash")
	if c.flashModelName != "custom-flash" {
		t.Errorf("flashModelName = %q, want 'custom-flash'", c.flashModelName)
	}
}

func TestShouldCompress(t *testing.T) {
	tests := []struct {
		name      string
		current   int
		max       int
		wantLayer CompressionLayer
		wantOk    bool
	}{
		{"zero max", 1000, 0, LayerToolGovernance, false},
		{"negative max", 1000, -1, LayerToolGovernance, false},
		{"below threshold", 100, 1000, LayerToolGovernance, false},
		{"at threshold", 800, 1000, LayerFullCompact, true},
		{"above threshold", 900, 1000, LayerFullCompact, true},
		{"exact 80%", 800, 1000, LayerFullCompact, true},
	}
	c := NewCompressionOrchestrator(nil, nil, "pro")
	for _, tt := range tests {
		layer, ok := c.ShouldCompress(tt.current, tt.max)
		if layer != tt.wantLayer || ok != tt.wantOk {
			t.Errorf("%s: ShouldCompress(%d,%d) = (%v,%v), want (%v,%v)",
				tt.name, tt.current, tt.max, layer, ok, tt.wantLayer, tt.wantOk)
		}
	}
}

func TestCompress_NoOpOnToolGovernance(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	history := []Message{{Role: "user", Content: "hello"}}
	result, err := c.Compress(context.Background(), LayerToolGovernance, nil, history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected unchanged history length, got %d", len(result))
	}
}

func TestCompress_NoModel(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	history := []Message{{Role: "user", Content: "hello"}}
	result, err := c.Compress(context.Background(), LayerFullCompact, nil, history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected unchanged history with no model, got %d", len(result))
	}
}

// --- findSafeSplitPoint ---

func TestFindSafeSplitPoint(t *testing.T) {
	tests := []struct {
		name     string
		history  []Message
		minFresh int
		wantIdx  int // split before this index
	}{
		{
			name:     "empty",
			history:  []Message{},
			minFresh: 10,
			wantIdx:  0,
		},
		{
			name: "short history, no minFresh match",
			history: []Message{
				{Role: "user", Content: "hi"},
			},
			minFresh: 10,
			wantIdx:  0,
		},
		{
			name: "user message boundary",
			history: []Message{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "response"},
				{Role: "user", Content: "second"},
				{Role: "assistant", Content: "response2"},
				{Role: "user", Content: "third"},
			},
			minFresh: 3,
			wantIdx:  3, // split before assistant "response2" (after user "second")
		},
		{
			name: "assistant without tool calls after user",
			history: []Message{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "reply"},
			},
			minFresh: 1,
			wantIdx:  0, // split before user; assistant follows user so returns i-1=0
		},
		{
			name: "tool call assistant not a safe split",
			history: []Message{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "reply", ToolCalls: []MessageToolCall{{ID: "c1"}}},
			},
			minFresh: 1,
			wantIdx:  1, // can't split on assistant with tool calls, falls through to user boundary
		},
	}
	for _, tt := range tests {
		got := findSafeSplitPoint(tt.history, tt.minFresh)
		if got != tt.wantIdx {
			t.Errorf("%s: findSafeSplitPoint = %d, want %d", tt.name, got, tt.wantIdx)
		}
	}
}

// findSafeSplitPoint expects minFresh >= 0; negative values trigger production code bug (out of bounds)

// --- extractPreviousArchive ---

func TestExtractPreviousArchive(t *testing.T) {
	tests := []struct {
		name    string
		history []Message
		want    string
	}{
		{
			name: "no archive",
			history: []Message{
				{Role: "user", Content: "hello"},
			},
			want: "",
		},
		{
			name: "with archive",
			history: []Message{
				{Role: "user", Content: "hello"},
				{Role: "system", Content: "[SESSION ARCHIVE]\ntest summary"},
			},
			want: "test summary",
		},
		{
			name: "multiple archives, returns latest",
			history: []Message{
				{Role: "system", Content: "[SESSION ARCHIVE]\nolder"},
				{Role: "user", Content: "middle"},
				{Role: "system", Content: "[SESSION ARCHIVE]\nnewer"},
			},
			want: "newer",
		},
	}
	for _, tt := range tests {
		got := extractPreviousArchive(tt.history)
		if got != tt.want {
			t.Errorf("%s: extractPreviousArchive = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// --- ParseArchiveSummary ---

func TestParseArchiveSummary(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantOK   bool
		wantGoal string
	}{
		{
			name:     "valid json",
			input:    `{"goal":"fix bug","decisions":["use X"],"key_findings":["found Y"],"open_issues":[]}`,
			wantOK:   true,
			wantGoal: "fix bug",
		},
		{
			name:     "wrapped in markdown",
			input:    "```json\n{\"goal\":\"refactor\"}\n```",
			wantOK:   true,
			wantGoal: "refactor",
		},
		{
			name:   "invalid json",
			input:  `not json`,
			wantOK: false,
		},
		{
			name:   "empty object",
			input:  `{}`,
			wantOK: true,
		},
	}
	for _, tt := range tests {
		summary, err := ParseArchiveSummary(tt.input)
		if (err == nil) != tt.wantOK {
			t.Errorf("%s: ParseArchiveSummary err = %v, wantOK=%v", tt.name, err, tt.wantOK)
			continue
		}
		if tt.wantOK && summary.Goal != tt.wantGoal {
			t.Errorf("%s: Goal = %q, want %q", tt.name, summary.Goal, tt.wantGoal)
		}
	}
}

// --- containsDecisionText ---

func TestContainsDecisionText(t *testing.T) {
	tests := []struct {
		name      string
		decisions []Decision
		text      string
		want      bool
	}{
		{"empty", nil, "foo", false},
		{"found", []Decision{{ID: "d1", Text: "use X"}}, "use X", true},
		{"not found", []Decision{{ID: "d1", Text: "use X"}}, "use Y", false},
		{"multiple, found", []Decision{{ID: "d1", Text: "a"}, {ID: "d2", Text: "b"}}, "b", true},
	}
	for _, tt := range tests {
		if got := containsDecisionText(tt.decisions, tt.text); got != tt.want {
			t.Errorf("%s: containsDecisionText = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// --- buildArchivePrompt ---

func TestBuildArchivePrompt_IncludesGoal(t *testing.T) {
	state := &TaskState{Goal: "add authentication"}
	history := []Message{{Role: "user", Content: "hello"}}
	prompt := buildArchivePrompt(state, history, false)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strContains(prompt, "add authentication") {
		t.Error("prompt should contain the goal")
	}
}

func TestBuildArchivePrompt_NoGoal(t *testing.T) {
	state := &TaskState{}
	history := []Message{{Role: "user", Content: "hello"}}
	prompt := buildArchivePrompt(state, history, false)
	// Should not contain empty goal string
	if strContains(prompt, "Task goal:") {
		t.Error("prompt should not show empty goal")
	}
}

func TestBuildArchivePrompt_SkipsSessionArchive(t *testing.T) {
	state := &TaskState{}
	history := []Message{
		{Role: "system", Content: "[SESSION ARCHIVE]\nold stuff"},
		{Role: "user", Content: "new message"},
	}
	prompt := buildArchivePrompt(state, history, false)
	if strContains(prompt, "[SESSION ARCHIVE]") {
		t.Error("prompt should skip SESSION ARCHIVE messages")
	}
	if !strContains(prompt, "new message") {
		t.Error("prompt should include non-archive messages")
	}
}

func TestBuildArchivePrompt_IncludesMemoryMarkers(t *testing.T) {
	state := &TaskState{MemoryMarkers: []string{"key: config.go", "note: use X"}}
	history := []Message{{Role: "user", Content: "hello"}}
	prompt := buildArchivePrompt(state, history, false)
	if !strContains(prompt, "key: config.go") {
		t.Error("prompt should include memory markers")
	}
	if !strContains(prompt, "note: use X") {
		t.Error("prompt should include all memory markers")
	}
}

func TestBuildArchivePrompt_IncludesDecisionsAndOpenIssues(t *testing.T) {
	state := &TaskState{
		Decisions:     []Decision{{ID: "d1", Text: "use X library"}},
		OpenQuestions: []string{"how to handle error"},
	}
	history := []Message{{Role: "user", Content: "hello"}}
	prompt := buildArchivePrompt(state, history, false)
	if !strContains(prompt, "use X library") {
		t.Error("prompt should include decisions")
	}
	if !strContains(prompt, "how to handle error") {
		t.Error("prompt should include open issues")
	}
}

func TestBuildArchivePrompt_NoMemoryMarkers(t *testing.T) {
	state := &TaskState{MemoryMarkers: nil}
	history := []Message{{Role: "user", Content: "hello"}}
	prompt := buildArchivePrompt(state, history, false)
	if strContains(prompt, "Memory markers") {
		t.Error("prompt should not mention memory markers when empty")
	}
}

// --- buildModelArchivePrompt ---

func TestBuildModelArchivePrompt(t *testing.T) {
	goal := "fix bug"
	history := []ModelMessage{{Role: "user", Content: "hello"}}
	prompt := buildModelArchivePrompt(goal, history, false)
	if !strContains(prompt, "fix bug") {
		t.Error("prompt should contain goal")
	}
	if !strContains(prompt, "hello") {
		t.Error("prompt should contain history")
	}
}

func TestBuildModelArchivePrompt_ExtractsPreviousArchive(t *testing.T) {
	goal := "fix"
	history := []ModelMessage{
		{Role: "system", Content: "[SESSION ARCHIVE]\nprev summary"},
		{Role: "user", Content: "new msg"},
	}
	prompt := buildModelArchivePrompt(goal, history, false)
	if !strContains(prompt, "prev summary") {
		t.Error("prompt should include previous archive")
	}
	if strContains(prompt, "[SESSION ARCHIVE]") {
		t.Error("prompt should skip archive messages in content")
	}
}

func TestBuildModelArchivePrompt_EmptyGoal(t *testing.T) {
	history := []ModelMessage{{Role: "user", Content: "hi"}}
	prompt := buildModelArchivePrompt("", history, false)
	if strContains(prompt, "Task goal:") {
		t.Error("empty goal should not be mentioned")
	}
}

// --- EstimateTokens ---

func TestEstimateTokens_NilEstimator(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	// Estimator is nil, so EstimateToken should return 0
	msgs := []ModelMessage{{Role: "user", Content: "hello"}}
	if n := c.EstimateTokens(msgs); n != 0 {
		t.Errorf("expected 0 with nil estimator, got %d", n)
	}
}

func TestCompressModelMessages_NoOpOnToolGovernance(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	history := []ModelMessage{{Role: "user", Content: "hello"}}
	result, err := c.CompressModelMessages(context.Background(), LayerToolGovernance, "goal", history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected unchanged history, got %d", len(result))
	}
}

func TestCompressModelMessages_NoModel(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	history := []ModelMessage{{Role: "user", Content: "hello"}}
	result, err := c.CompressModelMessages(context.Background(), LayerFullCompact, "goal", history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected unchanged with no model, got %d", len(result))
	}
}

// helpers

func strContains(s, substr string) bool {
	return len(s) >= len(substr) && indexOfStr(s, substr) >= 0
}

func indexOfStr(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Ensure imports are used
var _ = json.RawMessage{}
var _ = time.Nanosecond

// --- C8 回归测试：sub-agent 压缩必须裁剪旧历史 ---

// TestCompressModelMessages_TrimsOldHistory 回归 C8 缺陷：compressModelArchive
// 必须裁剪旧历史，把上下文压到 tailBudget 量级，而不是把 [SESSION ARCHIVE]
// 拼在全量历史之前。历史必须大到超过 tailBudget，否则压缩本就不该发生。
func TestCompressModelMessages_TrimsOldHistory(t *testing.T) {
	mock := &stubCompleteModel{resp: `{"goal":"g","decisions":[],"key_findings":["k1"]}`}
	c := NewCompressionOrchestrator(mock, nil, "pro")
	// 60 条 × 3000 字符 ≈ 60K tokens，远超 tailBudget(16384)。
	history := make([]ModelMessage, 0, 60)
	for i := 0; i < 60; i++ {
		history = append(history, ModelMessage{Role: "user", Content: strings.Repeat("m", 3000)})
	}
	beforeChars := 0
	for _, m := range history {
		beforeChars += len(m.Content)
	}
	result, err := c.CompressModelMessages(context.Background(), LayerFullCompact, "goal", history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) >= len(history) {
		t.Fatalf("compression must shrink history: len(result)=%d, len(history)=%d", len(result), len(history))
	}
	afterChars := 0
	for _, m := range result {
		afterChars += len(m.Content)
	}
	if afterChars > beforeChars/2 {
		t.Errorf("compression must keep only the tailBudget tail: kept %d/%d chars (%.1f%%)",
			afterChars, beforeChars, 100*float64(afterChars)/float64(beforeChars))
	}
	// 摘要必须保留在头部
	if !strings.HasPrefix(result[0].Content, "[SESSION ARCHIVE]") {
		t.Errorf("result[0] must be the archive summary, got %q", result[0].Content)
	}
}

// TestCompressModelMessages_ShortHistoryNoOp 验证短历史不触发压缩（返回原历史）。
func TestCompressModelMessages_ShortHistoryNoOp(t *testing.T) {
	mock := &stubCompleteModel{resp: `{"goal":"g"}`}
	c := NewCompressionOrchestrator(mock, nil, "pro")
	history := []ModelMessage{{Role: "user", Content: "hi"}}
	result, err := c.CompressModelMessages(context.Background(), LayerFullCompact, "goal", history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || result[0].Content != "hi" {
		t.Errorf("short history must be returned unchanged, got %+v", result)
	}
}

// TestCompressModelMessages_ModelErrorKeepsHistory 验证摘要生成失败时返回原历史（best-effort）。
func TestCompressModelMessages_ModelErrorKeepsHistory(t *testing.T) {
	mock := &stubCompleteModel{err: fmt.Errorf("flash down")}
	c := NewCompressionOrchestrator(mock, nil, "pro")
	// Large enough that compression actually runs and the model is consulted.
	history := make([]ModelMessage, 0, 60)
	for i := 0; i < 60; i++ {
		history = append(history, ModelMessage{Role: "user", Content: strings.Repeat("x", 3000)})
	}
	result, err := c.CompressModelMessages(context.Background(), LayerFullCompact, "goal", history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != len(history) {
		t.Errorf("model failure must keep history unchanged: got %d, want %d", len(result), len(history))
	}
}

// TestCompressionCooldown 回归验证：摘要生成失败后进入冷却窗口，期间
// ShouldCompress 返回 false（不再每轮重试失败的压缩），且冷却窗口内成功后清除。
func TestCompressionCooldown(t *testing.T) {
	c := NewCompressionOrchestrator(nil, nil, "pro")
	if c.isDegraded() {
		t.Fatal("fresh orchestrator must not be degraded")
	}
	// 失败 → 进入冷却
	c.markDegraded()
	if !c.isDegraded() {
		t.Fatal("markDegraded must set the degraded flag")
	}
	if _, ok := c.ShouldCompress(999999, 1000000); ok {
		t.Fatal("ShouldCompress must be suppressed while degraded")
	}
	// 成功 → 清除冷却，恢复正常压缩
	c.clearDegraded()
	if c.isDegraded() {
		t.Fatal("clearDegraded must reset the degraded flag")
	}
	if layer, ok := c.ShouldCompress(999999, 1000000); !ok || layer != LayerFullCompact {
		t.Fatalf("after clearing, ShouldCompress must allow compression, got layer=%v ok=%v", layer, ok)
	}
}

// --- findSafeSplitPoint 边界：负数 / 超大 minFresh 不得越界 panic ---

func TestFindSafeSplitPoint_NegativeMinFresh(t *testing.T) {
	history := []Message{{Role: "user", Content: "hi"}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("findSafeSplitPoint panicked with negative minFresh: %v", r)
		}
	}()
	if got := findSafeSplitPoint(history, -1); got != 0 {
		t.Errorf("findSafeSplitPoint(history, -1) = %d, want 0", got)
	}
}

func TestFindSafeSplitPoint_MinFreshExceedsLen(t *testing.T) {
	history := []Message{{Role: "user", Content: "hi"}}
	if got := findSafeSplitPoint(history, 100); got != 0 {
		t.Errorf("findSafeSplitPoint(history, 100) = %d, want 0", got)
	}
}

// --- A: 切分点按 token 预算（回归 len(history)-10 语义反转）---

// TestCompressArchive_ShrinksToTailBudget 锁定修复后的核心行为：压缩必须把
// 上下文裁到 tailBudget 量级，而不是保留约 96%（旧 len(history)-10 的行为）。
func TestCompressArchive_ShrinksToTailBudget(t *testing.T) {
	mock := &stubCompleteModel{resp: `{"goal":"g","key_findings":["k"]}`}
	c := NewCompressionOrchestrator(mock, nil, "pro")

	// 300 条 × 4000 字符 = 1.2M 字符（约 400K token），远超 tailBudget。
	history := make([]Message, 0, 300)
	for i := 0; i < 300; i++ {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		history = append(history, Message{Role: role, Content: strings.Repeat("x", 4000)})
	}
	beforeChars := 0
	for _, m := range history {
		beforeChars += len(m.Content)
	}

	result, err := c.Compress(context.Background(), LayerFullCompact, &TaskState{}, history)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	afterChars := 0
	for _, m := range result {
		afterChars += len(m.Content)
	}
	if afterChars > beforeChars*15/100 {
		t.Fatalf("compression kept %d/%d chars (%.1f%%), want <15%%",
			afterChars, beforeChars, 100*float64(afterChars)/float64(beforeChars))
	}
	if len(result) >= len(history) {
		t.Fatalf("len(result)=%d must be < len(history)=%d", len(result), len(history))
	}
	if !strings.HasPrefix(result[0].Content, "[SESSION ARCHIVE]") {
		t.Fatalf("result[0] must be the archive summary, got %q", result[0].Content)
	}
}

// TestCompressArchive_NoOpWhenHistoryFitsTail 验证整段历史本就装得下 tailBudget
// 时不做无意义压缩（避免以空摘要替换完整历史）。
func TestCompressArchive_NoOpWhenHistoryFitsTail(t *testing.T) {
	mock := &stubCompleteModel{resp: `{"goal":"g"}`}
	c := NewCompressionOrchestrator(mock, nil, "pro")
	history := make([]Message, 0, 20)
	for i := 0; i < 20; i++ {
		history = append(history, Message{Role: "user", Content: "hi"})
	}
	result, err := c.Compress(context.Background(), LayerFullCompact, &TaskState{}, history)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if len(result) != len(history) {
		t.Fatalf("history within tailBudget must be returned unchanged: got %d, want %d", len(result), len(history))
	}
}

func TestTailFreshCount(t *testing.T) {
	// 每条 3000 字符 ≈ 1001 token；预算 4000 token → 3 条。
	history := make([]Message, 0, 10)
	for i := 0; i < 10; i++ {
		history = append(history, Message{Role: "user", Content: strings.Repeat("x", 3000)})
	}
	if got := tailFreshCount(history, 4000); got != 3 {
		t.Errorf("tailFreshCount = %d, want 3", got)
	}
	// 单条就超预算时至少保留一条，避免切分点越界。
	if got := tailFreshCount(history, 1); got != 1 {
		t.Errorf("tailFreshCount with tiny budget = %d, want 1", got)
	}
}

// --- C: 摘要请求输入上限（分块渐进摘要）---

// countingCompleteModel counts Complete calls and records the largest request.
type countingCompleteModel struct {
	resp     string
	calls    int
	maxChars int
}

func (m *countingCompleteModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *countingCompleteModel) Complete(_ context.Context, req ModelRequest) (*ModelResponse, error) {
	m.calls++
	chars := 0
	for _, msg := range req.Messages {
		chars += modelMessageChars(msg)
	}
	if chars > m.maxChars {
		m.maxChars = chars
	}
	return &ModelResponse{Message: ModelMessage{Content: m.resp}}, nil
}

func TestSummarizeInChunks_BoundsEachRequest(t *testing.T) {
	mock := &countingCompleteModel{resp: `{"goal":"g"}`}
	c := NewCompressionOrchestrator(mock, nil, "pro")
	// 100 条 × 9000 字符 ≈ 300K token：单次摘要会吞下整段，必须分块。
	old := make([]Message, 0, 100)
	for i := 0; i < 100; i++ {
		old = append(old, Message{Role: "user", Content: strings.Repeat("x", 9000)})
	}
	summary, err := c.summarizeInChunks(context.Background(), &TaskState{}, old)
	if err != nil {
		t.Fatalf("summarizeInChunks: %v", err)
	}
	if summary == "" {
		t.Fatal("expected a summary")
	}
	if mock.calls < 2 {
		t.Fatalf("expected the segment to be split into multiple chunks, got %d call(s)", mock.calls)
	}
	// 单次请求 = 分块上限 + 单条消息上限 + 模板余量，远小于整段。
	if limit := summaryChunkTokens*3 + archiveMessageCharCap + 20000; mock.maxChars > limit {
		t.Errorf("a summary request was too large: %d chars (limit %d)", mock.maxChars, limit)
	}
}

// --- B: 发送前硬预算裁剪 ---

func TestTrimHistoryToBudget_DropsOldestKeepsSystemAndPairing(t *testing.T) {
	history := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("a", 3000)},
		{Role: "assistant", ToolCalls: []MessageToolCall{{ID: "c1", Name: "read"}}},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("b", 3000)},
		{Role: "user", Content: strings.Repeat("c", 3000)},
	}
	got := trimHistoryToBudget(history, 1200)
	if len(got) == 0 {
		t.Fatal("must keep something")
	}
	if got[0].Role != "system" {
		t.Fatalf("leading system message must be preserved, got role %q", got[0].Role)
	}
	if got[1].Role == "tool" {
		t.Fatalf("trimmed tail must not begin with an orphaned tool response")
	}
	if estimateHistoryTokens(got) > 1200 {
		t.Fatalf("trim did not reach the budget: %d tokens", estimateHistoryTokens(got))
	}
}

func TestTrimHistoryToBudget_TruncatesOversizedBody(t *testing.T) {
	// 一条巨大的 tool 消息：丢掉其它消息后仍超预算，只能截断正文（保留配对）。
	history := []Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []MessageToolCall{{ID: "c1", Name: "read"}}},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("z", 60000)},
	}
	got := trimHistoryToBudget(history, 3000)
	if estimateHistoryTokens(got) > 3000 {
		t.Fatalf("trim did not reach the budget: %d tokens", estimateHistoryTokens(got))
	}
	if len(got) == 0 || got[len(got)-1].Role != "tool" {
		t.Fatalf("the tool message (and its pairing) must survive truncation, got %d messages", len(got))
	}
}

func TestTrimModelHistoryToBudget_KeepsSystemHead(t *testing.T) {
	history := []ModelMessage{
		{Role: "system", Content: "persona"},
		{Role: "user", Content: strings.Repeat("a", 3000)},
		{Role: "assistant", ToolCalls: []ModelToolCall{{ID: "c1", Function: ModelFunctionCall{Name: "read"}}}},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("b", 3000)},
	}
	got := trimModelHistoryToBudget(history, 1200)
	if len(got) == 0 || got[0].Role != "system" {
		t.Fatalf("system head must be preserved, got %d messages", len(got))
	}
	if len(got) > 1 && got[1].Role == "tool" {
		t.Fatalf("trimmed tail must not begin with an orphaned tool response")
	}
}
