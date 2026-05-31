package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var setCmd = &cobra.Command{
	Use:   "set [key] [value]",
	Short: "Set configuration (e.g., deepact set api-key sk-xxx)",
	Args:  cobra.ExactArgs(2),
	RunE:  runSet,
}

func init() {
	rootCmd.AddCommand(setCmd)
}

func runSet(cmd *cobra.Command, args []string) error {
	key, value := args[0], args[1]
	switch key {
	case "api-key", "apikey", "key":
		if err := storeAPIKey(value); err != nil {
			return fmt.Errorf("store key: %w", err)
		}
		fmt.Printf("API key saved to %s\n", apiKeyPath())
		return nil
	default:
		return fmt.Errorf("unknown setting: %q (supported: api-key)", key)
	}
}
