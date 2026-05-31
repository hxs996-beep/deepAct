package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deepact/deepact/engine"
	"github.com/spf13/cobra"
)

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Query evaluation records and prompt performance",
	Long: `Inspect historical evaluation data collected from code reviews.
Records are stored as JSONL in ~/.deepact/eval/records.jsonl.

Subcommands:
  history   List recent evaluation records
  stats     Overall evaluation statistics
  compare   Compare two prompt versions`,
}

var evalHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "List recent evaluation records",
	RunE:  runEvalHistory,
}

var evalStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show overall evaluation statistics",
	RunE:  runEvalStats,
}

var evalCompareCmd = &cobra.Command{
	Use:   "compare [v1] [v2]",
	Short: "Compare two prompt versions",
	Args:  cobra.ExactArgs(2),
	RunE:  runEvalCompare,
}

func init() {
	evalCmd.AddCommand(evalHistoryCmd)
	evalCmd.AddCommand(evalStatsCmd)
	evalCmd.AddCommand(evalCompareCmd)
	rootCmd.AddCommand(evalCmd)

	evalHistoryCmd.Flags().Int("limit", 20, "number of records to show")
	evalHistoryCmd.Flags().String("phase", "", "filter by phase (e.g. verification_review)")
	evalHistoryCmd.Flags().Bool("passed", false, "show only passed records")
	evalHistoryCmd.Flags().Bool("failed", false, "show only failed records")
}

func openEvalStore() (engine.EvalStore, string, error) {
	evalPath := filepath.Join(evalDir(), "records.jsonl")
	store, err := engine.NewJSONLEvalStore(evalPath)
	if err != nil {
		return nil, "", fmt.Errorf("open eval store at %s: %w", evalPath, err)
	}
	return store, evalPath, nil
}

func runEvalHistory(cmd *cobra.Command, args []string) error {
	store, path, err := openEvalStore()
	if err != nil {
		return err
	}
	defer store.Close()

	limit, _ := cmd.Flags().GetInt("limit")
	phase, _ := cmd.Flags().GetString("phase")
	showPassed, _ := cmd.Flags().GetBool("passed")
	showFailed, _ := cmd.Flags().GetBool("failed")

	var passedFilter *bool
	if showPassed {
		t := true
		passedFilter = &t
	} else if showFailed {
		t := false
		passedFilter = &t
	}

	records, err := store.Query(engine.EvalFilter{
		Limit:  limit,
		Phase:  phase,
		Passed: passedFilter,
	})
	if err != nil {
		return fmt.Errorf("query records: %w", err)
	}

	if len(records) == 0 {
		fmt.Println("📊 No evaluation records found.")
		fmt.Printf("   Store: %s\n", path)
		return nil
	}

	fmt.Printf("📊 Evaluation Records (last %d)\n", limit)
	fmt.Printf("   Store: %s\n\n", path)

	// Table header
	fmt.Printf("%-20s | %-8s | %-24s | %-6s | %-4s | %s\n",
		"Timestamp", "Phase", "Goal", "Score", "Pass", "Prompt Version")
	fmt.Println(strings.Repeat("-", 110))

	for _, r := range records {
		ts := r.Timestamp.Format("2006-01-02 15:04")
		phase := r.Phase
		if len(phase) > 8 {
			phase = phase[:8]
		}
		goal := r.GoalSnippet
		if len(goal) > 24 {
			goal = goal[:24]
		}
		passMark := "✅"
		if !r.Passed {
			passMark = "❌"
		}
		pv := r.PromptVersion
		if len(pv) > 16 {
			pv = pv[:16]
		}
		fmt.Printf("%-20s | %-8s | %-24s | %5.1f  | %-4s | %s\n",
			ts, phase, goal, r.TotalScore, passMark, pv)
	}
	return nil
}

func runEvalStats(cmd *cobra.Command, args []string) error {
	store, path, err := openEvalStore()
	if err != nil {
		return err
	}
	defer store.Close()

	stats, err := store.Stats()
	if err != nil {
		return fmt.Errorf("get stats: %w", err)
	}

	fmt.Println("📊 Evaluation Statistics")
	fmt.Printf("   Store: %s\n\n", path)

	if stats.TotalRecords == 0 {
		fmt.Println("No records yet. Start using DeepAct to collect evaluation data.")
		return nil
	}

	fmt.Printf("Total evaluations: %d\n", stats.TotalRecords)
	fmt.Printf("Date range:        %s — %s\n",
		stats.EarliestRecord.Format("2006-01-02"),
		stats.LatestRecord.Format("2006-01-02"))
	fmt.Printf("Average score:     %.1f/100\n", stats.AverageScore)
	fmt.Printf("Pass rate:         %d/%d (%.1f%%)\n",
		stats.PassCount, stats.TotalRecords, stats.PassRate)
	fmt.Printf("Avg tokens/eval:   %d\n\n", stats.AverageTokens)

	// Per-phase breakdown
	if len(stats.ByPhase) > 0 {
		fmt.Println("By Phase:")
		fmt.Printf("  %-24s %5s %8s %8s\n", "Phase", "Count", "AvgScore", "PassRate")
		for phase, ps := range stats.ByPhase {
			passRate := float64(ps.PassCount) / float64(ps.Count) * 100
			fmt.Printf("  %-24s %5d %8.1f %7.1f%%\n", phase, ps.Count, ps.AverageScore, passRate)
		}
		fmt.Println()
	}

	// Per-version breakdown
	if len(stats.ByPromptVer) > 0 {
		fmt.Println("By Prompt Version:")
		fmt.Printf("  %-16s %5s %8s %8s\n", "Version", "Count", "AvgScore", "PassRate")
		for ver, pv := range stats.ByPromptVer {
			fmt.Printf("  %-16s %5d %8.1f %7.1f%%\n", ver, pv.Count, pv.AverageScore, pv.PassRate)
		}
		fmt.Println()
	}

	// ASCII trend: last 10 records by time bucket
	if stats.TotalRecords >= 3 {
		fmt.Println("Score Trend (recent records):")
		all, _ := store.Query(engine.EvalFilter{Limit: 20})
		// Show a simple sparkline
		var bars []string
		for i := len(all) - 1; i >= 0; i-- {
			score := all[i].TotalScore
			barLen := int(score / 10)
			bar := strings.Repeat("█", barLen)
			passSym := "✓"
			if !all[i].Passed {
				passSym = "✗"
			}
			ts := all[i].Timestamp.Format("01-02 15:04")
			bars = append(bars, fmt.Sprintf("  %s %s %5.1f %s", ts, passSym, score, bar))
		}
		for _, b := range bars {
			fmt.Println(b)
		}
	}

	return nil
}

func runEvalCompare(cmd *cobra.Command, args []string) error {
	store, path, err := openEvalStore()
	if err != nil {
		return err
	}
	defer store.Close()

	v1, v2 := args[0], args[1]

	recs1, err := store.Query(engine.EvalFilter{PromptVer: v1})
	if err != nil {
		return fmt.Errorf("query v1: %w", err)
	}
	recs2, err := store.Query(engine.EvalFilter{PromptVer: v2})
	if err != nil {
		return fmt.Errorf("query v2: %w", err)
	}

	fmt.Printf("📊 Prompt Version Comparison\n")
	fmt.Printf("   Store: %s\n\n", path)

	if len(recs1) == 0 && len(recs2) == 0 {
		fmt.Println("No records found for either version.")
		return nil
	}

	calcStats := func(recs []engine.EvalRecord) (count int, avgScore float64, passRate float64) {
		if len(recs) == 0 {
			return
		}
		count = len(recs)
		var totalScore float64
		var passCount int
		for _, r := range recs {
			totalScore += r.TotalScore
			if r.Passed {
				passCount++
			}
		}
		avgScore = totalScore / float64(count)
		passRate = float64(passCount) / float64(count) * 100
		return
	}

	c1, s1, pr1 := calcStats(recs1)
	c2, s2, pr2 := calcStats(recs2)

	fmt.Printf("%-16s %8s %10s %10s\n", "Version", "Count", "AvgScore", "PassRate")
	fmt.Printf("%-16s %8d %10.1f %9.1f%%\n", v1, c1, s1, pr1)
	fmt.Printf("%-16s %8d %10.1f %9.1f%%\n", v2, c2, s2, pr2)

	if c1 > 0 && c2 > 0 {
		diff := s2 - s1
		dir := "higher"
		if diff < 0 {
			dir = "lower"
			diff = -diff
		}
		fmt.Printf("\n%s avg score is %.1f points %s than %s\n", v2, diff, dir, v1)
	}

	return nil
}

func evalDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".deepact", "eval")
	}
	return filepath.Join(os.TempDir(), "deepact", "eval")
}
