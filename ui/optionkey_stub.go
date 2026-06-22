//go:build (!darwin || !cgo || !optionkey) && !windows

package ui

func optionKeyPressed() bool { return false }
func ctrlKeyPressed() bool { return false }
