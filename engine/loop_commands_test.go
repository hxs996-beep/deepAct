package engine

import "testing"

func TestIsDangerousConfirmation(t *testing.T) {
	tests := []struct {
		msg string
		exp bool
	}{
		{"确认", true}, {"yes", true}, {"好的", true}, {"y", true},
		{"对，改吧", true}, {"好的，执行", true}, {"ok, go", true},
		{"确认执行", true}, {"继续执行", true}, {"继续", true}, {"执行吧", true},
		{"确认执行修改", true}, {"确认修改", true}, {"执行修改", true},
		{"确认但先改下方案", false}, {"改一下方案再执行", false}, {"修改一下配置", false},
		{"修改", false}, {"你确认下对不对", false}, {"did", false}, {"你好", false},
	}
	for _, tt := range tests {
		if got := isDangerousConfirmation(tt.msg); got != tt.exp {
			t.Errorf("isDangerousConfirmation(%q) = %v, want %v", tt.msg, got, tt.exp)
		}
	}
}

func TestIsClearCommand(t *testing.T) {
	tests := []struct {
		msg string
		exp bool
	}{
		{"/clear", true},
		{"/clear ", true},
		{"/clear all the things", true},
		{"/Clear", false},
		{"clear", false},
		{"/clear  extra", true},
	}
	for _, tt := range tests {
		if got := isClearCommand(tt.msg); got != tt.exp {
			t.Errorf("isClearCommand(%q) = %v, want %v", tt.msg, got, tt.exp)
		}
	}
}
