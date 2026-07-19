package engine

import (
	"context"
	"strings"
	"testing"
)

func TestIntentClassifier_Analyze(t *testing.T) {
	m := &stubCompleteModel{resp: `{"intent": "analyze"}`}
	c := NewIntentClassifier(m, "flash-model", true)
	got, err := c.Classify(context.Background(), IntentCheck{Goal: "添加登录页面", Message: "分析一下这个接口为什么报错"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != IntentAnalyze {
		t.Errorf("expected IntentAnalyze, got %v", got)
	}
}

func TestIntentClassifier_Continue(t *testing.T) {
	m := &stubCompleteModel{resp: `{"intent": "continue"}`}
	c := NewIntentClassifier(m, "flash-model", true)
	got, err := c.Classify(context.Background(), IntentCheck{Goal: "添加登录页面", Message: "刚才那个再调整一下"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != IntentContinue {
		t.Errorf("expected IntentContinue, got %v", got)
	}
}

func TestIntentClassifier_NewTopic(t *testing.T) {
	m := &stubCompleteModel{resp: `{"intent": "new_topic"}`}
	c := NewIntentClassifier(m, "flash-model", true)
	got, err := c.Classify(context.Background(), IntentCheck{Goal: "添加登录页面", Message: "重构数据库查询"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != IntentNewTopic {
		t.Errorf("expected IntentNewTopic, got %v", got)
	}
}

func TestIntentClassifier_BadJSON_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{resp: `not json`}
	c := NewIntentClassifier(m, "flash-model", true)
	_, err := c.Classify(context.Background(), IntentCheck{Goal: "goal", Message: "msg"})
	if err == nil {
		t.Fatalf("expected error for non-JSON response, got nil")
	}
}

func TestIntentClassifier_CallError_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{err: errBoom}
	c := NewIntentClassifier(m, "flash-model", true)
	_, err := c.Classify(context.Background(), IntentCheck{Goal: "goal", Message: "msg"})
	if err == nil {
		t.Fatalf("expected error from Complete, got nil")
	}
}

func TestIntentClassifier_UnrecognizedIntent_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{resp: `{"intent": "unknown"}`}
	c := NewIntentClassifier(m, "flash-model", true)
	_, err := c.Classify(context.Background(), IntentCheck{Goal: "goal", Message: "msg"})
	if err == nil {
		t.Fatalf("expected error for unrecognized intent, got nil")
	}
}

func TestIntentClassifier_RequestShape(t *testing.T) {
	m := &stubCompleteModel{resp: `{"intent": "analyze"}`}
	c := NewIntentClassifier(m, "flash-model", false)
	_, _ = c.Classify(context.Background(), IntentCheck{Goal: "add login page", Message: "explain the logic"})
	req := m.last
	if req.Model != "flash-model" {
		t.Errorf("expected Model=flash-model, got %q", req.Model)
	}
	if !req.JsonMode {
		t.Errorf("expected JsonMode=true")
	}
	if req.Temperature != 0 {
		t.Errorf("expected Temperature=0, got %v", req.Temperature)
	}
	if req.MaxTokens != 64 {
		t.Errorf("expected MaxTokens=64, got %d", req.MaxTokens)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Errorf("expected system+user messages, got %+v", req.Messages)
	}
	if !strings.Contains(req.Messages[1].Content, "add login page") || !strings.Contains(req.Messages[1].Content, "explain the logic") {
		t.Errorf("expected user message to contain goal and message, got %q", req.Messages[1].Content)
	}
}

func TestIntentClassifier_ParsesNonPureJSON(t *testing.T) {
	tests := []struct {
		name string
		resp string
		want UserIntent
	}{
		{"markdown wrapped analyze", "```json\n{\"intent\": \"analyze\"}\n```", IntentAnalyze},
		{"markdown wrapped continue", "```json\n{\"intent\": \"continue\"}\n```", IntentContinue},
		{"prefix text then json", "根据分析，意图如下：\n{\"intent\": \"new_topic\"}", IntentNewTopic},
		{"suffix text after json", "{\"intent\": \"analyze\"}\n以上是判定。", IntentAnalyze},
		{"json with leading spaces", "   {\"intent\": \"continue\"}   ", IntentContinue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &stubCompleteModel{resp: tt.resp}
			c := NewIntentClassifier(m, "flash-model", true)
			got, err := c.Classify(context.Background(), IntentCheck{Goal: "g", Message: "m"})
			if err != nil {
				t.Fatalf("unexpected err for %s: %v (resp=%q)", tt.name, err, tt.resp)
			}
			if got != tt.want {
				t.Errorf("%s: got %v, want %v (resp=%q)", tt.name, got, tt.want, tt.resp)
			}
		})
	}
}
