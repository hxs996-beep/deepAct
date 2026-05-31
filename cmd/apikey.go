package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func loadAPIKey() (string, error) {
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		return key, nil
	}
	key, err := readStoredKey()
	if err == nil && key != "" {
		return key, nil
	}
	return "", nil
}

func readStoredKey() (string, error) {
	path := apiKeyPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func storeAPIKey(key string) error {
	path := apiKeyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(key), 0o600)
}

// promptForAPIKey is retained for non-TUI flows but unused by default.
func promptForAPIKey() (string, error) {
	fmt.Println("┌─────────────────────────────────────────┐")
	fmt.Println("│  Welcome to DeepAct!                    │")
	fmt.Println("│  DeepSeek API key required to continue. │")
	fmt.Println("│  Get one at: https://platform.deepseek.com │")
	fmt.Println("└─────────────────────────────────────────┘")
	fmt.Print("\nEnter your DEEPSEEK_API_KEY: ")

	reader := bufio.NewReader(os.Stdin)
	key, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("API key cannot be empty")
	}
	if err := storeAPIKey(key); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save key: %v\n", err)
	} else {
		fmt.Printf("Key saved to %s\n\n", apiKeyPath())
	}
	return key, nil
}

func apiKeyPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".deepact", "credentials")
	}
	return filepath.Join(os.TempDir(), "deepact", "credentials")
}
