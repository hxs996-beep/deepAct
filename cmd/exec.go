package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/deepact/deepact/engine"
	"github.com/spf13/cobra"
)

var execCmd = &cobra.Command{
	Use:   "exec [prompt]",
	Short: "Execute a task non-interactively (CI/headless mode)",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runHeadless,
}

func runHeadless(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	config, deps, err := buildEngineDeps()
	if err != nil {
		return err
	}
	agent := engine.NewEngine(config, deps)
	agent.SetStopHooks([]engine.StopHook{
		&engine.ZeroToolCallHook{MaxRetries: 5},
		&engine.StalledNarrationHook{MaxRetries: 4},
	})

	prompt := strings.Join(args, " ")
	response, err := agent.Run(ctx, prompt)
	if err != nil {
		return err
	}
	if response != nil {
		if response.Blocked {
			fmt.Println("Blocked:", response.BlockedBy)
			for _, q := range response.Questions {
				fmt.Println(q)
			}
			return nil
		}
		if response.Summary != "" {
			fmt.Println(response.Summary)
		}
	}
	return nil
}

func init() {
	rootCmd.AddCommand(execCmd)
	execCmd.Flags().String("output", "human", "output format: human | jsonl")
	execCmd.Flags().Int("max-turns", 30, "maximum agent turns before stopping")
}
