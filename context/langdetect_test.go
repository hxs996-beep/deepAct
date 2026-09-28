package context

import (
	"testing"

	"github.com/deepact/deepact/engine"
)

// TestClassifyTextLanguage pins the typed detection results: English must be
// LangEnglish (never an empty string), so downstream branches can rely on the
// value instead of encoding "English" as "no value".
func TestClassifyTextLanguage(t *testing.T) {
	tests := []struct {
		name string
		text string
		want engine.UserLanguage
	}{
		{"english", "fix the flaky test", engine.LangEnglish},
		{"chinese", "修复这个 bug", engine.LangChinese},
		{"mixed_leans_chinese", "帮我 fix the bug", engine.LangChinese},
		// Kana only: the kana branch is reachable only when the text has no Han
		// characters at all.
		{"japanese_kana_only", "テストがしっぱいする", engine.LangJapanese},
		// Known limitation of the heuristic (unchanged by this refactor): a Han
		// character wins over kana, so Japanese with kanji reads as Chinese.
		{"japanese_with_kanji_currently_chinese", "テストが失敗する", engine.LangChinese},
		{"fenced_code_only", "```go\nvar x = 1\n```", engine.LangEnglish},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyTextLanguage(tt.text); got != tt.want {
				t.Errorf("classifyTextLanguage(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

// TestDetectUserLanguage_LockedToFirstMessage: later English confirmations
// ("ok", "yes") must not flip the language locked from the first message.
func TestDetectUserLanguage_LockedToFirstMessage(t *testing.T) {
	history := []engine.Message{
		{Role: "user", Content: "修复登录失败"},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: "yes please continue"},
	}
	if got := detectUserLanguage(history); got != engine.LangChinese {
		t.Errorf("detectUserLanguage = %q, want %q", got, engine.LangChinese)
	}
}

// TestUserLanguageFor pins the flag → language mapping used by the engine when
// it derives the session language for handoffs and compression.
func TestUserLanguageFor(t *testing.T) {
	if got := engine.UserLanguageFor(true); !got.IsChinese() {
		t.Errorf("UserLanguageFor(true) = %q, want Chinese", got)
	}
	if got := engine.UserLanguageFor(false); got != engine.LangEnglish {
		t.Errorf("UserLanguageFor(false) = %q, want %q", got, engine.LangEnglish)
	}
}
