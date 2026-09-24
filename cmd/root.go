package cmd

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "deepact",
	Short: "DeepAct - AI coding agent for DeepSeek V4",
	Long:  "DeepAct: An interactive CLI coding agent built for DeepSeek V4.",
	RunE:  runInteractive,
}

func Execute() error {
	return rootCmd.Execute()
}
