package cmd

import (
	"fmt"
	"os"

	deeplogconfig "github.com/deepact/deepact/config"
)

// loadAPIKey resolves the API key from the DEEPSEEK_API_KEY env var or the
// api_key field in .deepact/config.toml. It never errors on a missing key —
// callers handle the empty-string case. A filesystem/lookup error is returned
// only when the working directory cannot be determined.
func loadAPIKey() (string, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working dir: %w", err)
	}
	return deeplogconfig.LoadAPIKey(workDir), nil
}

// storeAPIKey persists the API key to the user-level config file
// (~/.deepact/config.toml).
func storeAPIKey(key string) error {
	return deeplogconfig.SaveAPIKey(key)
}
