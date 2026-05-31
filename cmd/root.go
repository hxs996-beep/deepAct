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

func init() {
	rootCmd.PersistentFlags().String("config", "", "config file")
	rootCmd.PersistentFlags().String("model", "", "override model (flash/pro)")
	rootCmd.PersistentFlags().Bool("auto", false, "auto mode (skip confirmations)")
	rootCmd.PersistentFlags().Bool("verbose", false, "verbose output")
}
