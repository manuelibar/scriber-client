package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"scriber/internal/config"
	"scriber/internal/persist"
)

func historyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "history",
		Short: "Manage transcript history",
	}
	c.AddCommand(historyPruneCmd())
	return c
}

func historyPruneCmd() *cobra.Command {
	var transcriptsDir string
	var force bool
	var dryRun bool
	var empty bool
	var failed bool
	var successful bool
	var stream string
	var beforeRaw string
	var olderThanRaw string
	var keepLast int
	var orphanAudio bool

	c := &cobra.Command{
		Use:   "prune",
		Short: "Delete transcript history after previewing exact prune stats",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if failed && successful {
				return fmt.Errorf("--failed and --successful are mutually exclusive")
			}
			before, err := parseHistoryBefore(beforeRaw)
			if err != nil {
				return err
			}
			olderThan, err := parseHistoryDuration(olderThanRaw)
			if err != nil {
				return err
			}

			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			dir := cfg.Storage.TranscriptsDir
			if transcriptsDir != "" {
				dir = config.ExpandPath(transcriptsDir)
			}

			filter := persist.HistoryPruneFilter{
				Empty:              empty,
				Failed:             failed,
				Successful:         successful,
				Stream:             stream,
				Before:             before,
				OlderThan:          olderThan,
				KeepLast:           keepLast,
				IncludeOrphanAudio: orphanAudio,
			}
			plan, err := persist.PlanHistoryPrune(dir, filter)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			printHistoryPrunePlan(out, plan)
			if plan.FilesMatched() == 0 {
				return nil
			}
			if dryRun {
				fmt.Fprintln(out, "dry run: nothing deleted")
				return nil
			}
			if !force {
				ok, err := confirmHistoryPrune(cmd.InOrStdin(), out)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(out, "aborted")
					return nil
				}
			}
			result, err := plan.Apply()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "deleted:       %d files (%s)\n", result.FilesDeleted, formatBytes(result.BytesDeleted))
			return nil
		},
	}
	c.Flags().StringVar(&transcriptsDir, "transcripts-dir", "", "directory containing transcript JSON and WAV files")
	c.Flags().BoolVar(&force, "force", false, "delete without asking for confirmation")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show prune stats without deleting")
	c.Flags().BoolVar(&empty, "empty", false, "delete records with an empty transcript")
	c.Flags().BoolVar(&failed, "failed", false, "delete records that failed transcription or delivery")
	c.Flags().BoolVar(&successful, "successful", false, "delete successful records")
	c.Flags().StringVar(&stream, "stream", "", "delete records for a specific stream name")
	c.Flags().StringVar(&beforeRaw, "before", "", "delete records before a date/time (YYYY-MM-DD or RFC3339)")
	c.Flags().StringVar(&olderThanRaw, "older-than", "", "delete records older than a duration (for example 12h, 7d, 2w)")
	c.Flags().IntVar(&keepLast, "keep-last", 0, "keep the newest N records that otherwise match")
	c.Flags().BoolVar(&orphanAudio, "orphan-audio", false, "also delete WAV files with no matching transcript JSON")
	return c
}

func printHistoryPrunePlan(w io.Writer, plan *persist.HistoryPrunePlan) {
	fmt.Fprintf(w, "directory:     %s\n", plan.Directory)
	fmt.Fprintf(w, "records:       %d scanned, %d matched\n", plan.RecordsScanned, plan.RecordsMatched)
	fmt.Fprintf(w, "matched types: %d empty, %d failed, %d successful\n", plan.EmptyRecordsMatched, plan.FailedRecordsMatched, plan.SuccessfulRecordsMatched)
	fmt.Fprintf(w, "files:         %d total, %d json, %d audio", plan.FilesMatched(), plan.JSONFilesMatched, plan.AudioFilesMatched)
	if plan.OrphanAudioFilesMatched > 0 {
		fmt.Fprintf(w, " (%d orphan)", plan.OrphanAudioFilesMatched)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "size:          %s\n", formatBytes(plan.BytesMatched))
}

func confirmHistoryPrune(in io.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, "Type 'yes' to delete these transcript history files: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}

func parseHistoryBefore(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	var lastErr error
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, raw, time.Local)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, fmt.Errorf("before must be YYYY-MM-DD or RFC3339: %w", lastErr)
}

func parseHistoryDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d, nil
	}
	multiplier := time.Duration(0)
	unit := raw[len(raw)-1:]
	switch unit {
	case "d":
		multiplier = 24 * time.Hour
	case "w":
		multiplier = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("older-than must be a duration like 12h, 7d, or 2w")
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw[:len(raw)-1]))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("older-than must be a positive duration")
	}
	return time.Duration(n) * multiplier, nil
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}
