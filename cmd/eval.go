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

	// Separate "main_agent" efficiency records from conference scorecard records
	all, _ := store.Query(engine.EvalFilter{Limit: 10000})
	var agentRecs, scoreRecs []engine.EvalRecord
	for _, r := range all {
		if r.Phase == "main_agent" {
			agentRecs = append(agentRecs, r)
		} else {
			scoreRecs = append(scoreRecs, r)
		}
	}

	// --- ScoreCard stats ---
	if len(scoreRecs) > 0 {
		fmt.Printf("## ScoreCard Records\n")
		fmt.Printf("   %d records\n\n", len(scoreRecs))
		fmt.Printf("Average score:     %.1f/100\n", stats.AverageScore)
		fmt.Printf("Pass rate:         %d/%d (%.1f%%)\n",
			stats.PassCount, stats.TotalRecords, stats.PassRate)
		fmt.Printf("Avg tokens/eval:   %d\n\n", stats.AverageTokens)

		if len(stats.ByPhase) > 0 {
			fmt.Println("By Phase:")
			fmt.Printf("  %-24s %5s %8s %8s\n", "Phase", "Count", "AvgScore", "PassRate")
			for phase, ps := range stats.ByPhase {
				passRate := float64(ps.PassCount) / float64(ps.Count) * 100
				fmt.Printf("  %-24s %5d %8.1f %7.1f%%\n", phase, ps.Count, ps.AverageScore, passRate)
			}
			fmt.Println()
		}
	}

	// --- Main Agent efficiency stats ---
	if len(agentRecs) > 0 {
		fmt.Println("## 主 Agent 效率 (main_agent)")
		fmt.Printf("   %d records\n\n", len(agentRecs))

		var totalPrompt, totalComplete, totalHit, totalMiss int64
		var totalDur, totalTurns, totalCalls, totalFiles, totalErrors int64
		var totalCost float64
		for _, r := range agentRecs {
			totalPrompt += int64(r.PromptTokens)
			totalComplete += int64(r.CompletionTokens)
			totalHit += int64(r.CacheHitTokens)
			totalMiss += int64(r.CacheMissTokens)
			totalDur += r.DurationMs
			totalTurns += int64(r.IterationCount)
			totalCalls += int64(r.ToolCallCount)
			totalFiles += int64(r.ModifiedFileCount)
			totalErrors += int64(r.ErrorCount)
			totalCost += r.CostEstimate
		}
		n := int64(len(agentRecs))

		hitRate := 0.0
		if totalHit+totalMiss > 0 {
			hitRate = float64(totalHit) / float64(totalHit+totalMiss) * 100
		}

		fmt.Printf("平均轮次:       %5.1f 轮/任务\n", float64(totalTurns)/float64(n))
		fmt.Printf("平均耗时:       %5.1f 秒\n", float64(totalDur)/1000/float64(n))
		fmt.Printf("平均 prompt:    %5d tokens\n", totalPrompt/n)
		fmt.Printf("平均 completion:%5d tokens\n", totalComplete/n)
		fmt.Printf("缓存命中率:     %5.1f%% (%d hit / %d total)\n", hitRate, totalHit, totalHit+totalMiss)
		fmt.Printf("平均工具调用:   %5.1f 次\n", float64(totalCalls)/float64(n))
		fmt.Printf("平均修改文件:   %5.1f 个\n", float64(totalFiles)/float64(n))
		fmt.Printf("错误次数:       %5d (共 %d 次)\n", totalErrors, n)
		fmt.Printf("总预估费用:     $%.4f\n", totalCost)
		fmt.Printf("平均每任务费用: $%.4f\n\n", totalCost/float64(n))

		// Per-prompt-version efficiency breakdown
		if len(stats.ByPromptVer) > 0 {
			fmt.Println("按 Prompt 版本:")
			fmt.Printf("  %-16s %5s %8s %8s %8s\n", "Version", "Count", "Turns", "Duration", "Cache%")
			for ver, pv := range stats.ByPromptVer {
				_ = pv
				var vTurns, vDur, vHit, vMiss int64
				var vCount int
				for _, r := range agentRecs {
					if r.PromptVersion == ver {
						vTurns += int64(r.IterationCount)
						vDur += r.DurationMs
						vHit += int64(r.CacheHitTokens)
						vMiss += int64(r.CacheMissTokens)
						vCount++
					}
				}
				if vCount == 0 {
					continue
				}
				vHitRate := 0.0
				if vHit+vMiss > 0 {
					vHitRate = float64(vHit) / float64(vHit+vMiss) * 100
				}
				fmt.Printf("  %-16s %5d %8.1f %8.1fs %7.1f%%\n",
					ver, vCount, float64(vTurns)/float64(vCount), float64(vDur)/1000/float64(vCount), vHitRate)
			}
			fmt.Println()
		}
	}

	// --- History table (efficiency-focused for main_agent records) ---
	if len(agentRecs) > 0 {
		fmt.Println("最近记录:")
		fmt.Printf("%-20s | %-7s | %5s | %6s | %6s | %4s | %s\n",
			"Timestamp", "Turns", "Dur(s)", "Tokens", "Hit%", "Calls", "Goal")
		fmt.Println(strings.Repeat("-", 100))

		show := agentRecs
		if len(show) > 20 {
			show = show[:20]
		}
		for _, r := range show {
			ts := r.Timestamp.Format("01-02 15:04")
			dur := float64(r.DurationMs) / 1000
			total := r.PromptTokens + r.CompletionTokens
			hitRate := 0.0
			if r.CacheHitTokens+r.CacheMissTokens > 0 {
				hitRate = float64(r.CacheHitTokens) / float64(r.CacheHitTokens+r.CacheMissTokens) * 100
			}
			goal := r.GoalSnippet
			if len(goal) > 24 {
				goal = goal[:24]
			}
			errMark := ""
			if r.ErrorCount > 0 {
				errMark = "⚠️"
			}
			fmt.Printf("%-20s | %7d | %5.1f | %6d | %5.1f%% | %4d | %s %s\n",
				ts, r.IterationCount, dur, total, hitRate, r.ToolCallCount, goal, errMark)
		}
		fmt.Println()
	}

	// ASCII trend: last 10 agent records
	if len(agentRecs) >= 3 {
		fmt.Println("轮次趋势 (最近记录):")
		for i := len(agentRecs) - 1; i >= 0 && i >= len(agentRecs)-10; i-- {
			r := agentRecs[i]
			turns := r.IterationCount
			if turns > 20 {
				turns = 20
			}
			bar := strings.Repeat("▓", turns) + strings.Repeat("░", 20-turns)
			errSym := " "
			if r.ErrorCount > 0 {
				errSym = "⚠"
			}
			ts := r.Timestamp.Format("01-02 15:04")
			fmt.Printf("  %s %s %3d turns  %s\n", ts, errSym, r.IterationCount, bar)
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
