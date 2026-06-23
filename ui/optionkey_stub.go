//go:build !darwin && !windows

package ui

func optionKeyPressed() bool { return false }
func ctrlKeyPressed() bool { return false }
