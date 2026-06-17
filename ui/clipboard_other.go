//go:build !windows

package ui

func windowsCopyImpl(_ []uint16) error {
	return nil // unreachable on non-Windows
}

func utf16EncodeString(_ string) ([]uint16, error) {
	return nil, nil // unreachable on non-Windows
}
