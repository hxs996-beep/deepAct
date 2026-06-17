//go:build !darwin || !cgo || !optionkey

package ui

func optionKeyPressed() bool { return false }
