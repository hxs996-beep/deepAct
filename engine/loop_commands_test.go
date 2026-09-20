package engine

import "testing"

func TestIsConfirmCommand(t *testing.T) {
	tests := []struct {
		msg string
		exp bool
	}{
		{"/confirm 1", true}, {"/confirm 2", true}, {"/confirm 99", true},
		{"/confirm", false}, {"/confirm 0", false}, {"/confirm -1", false},
		{"confirm", false}, {"确认", false}, {"yes", false}, {"", false},
		{"/confirm 1 2", false}, {"  /confirm 3  ", true},
	}
	for _, tt := range tests {
		if got := isConfirmCommand(tt.msg); got != tt.exp {
			t.Errorf("isConfirmCommand(%q) = %v, want %v", tt.msg, got, tt.exp)
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
