package main

import (
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"scriber/internal/config"
	"scriber/internal/ipc"
	"scriber/internal/persist"
)

func attachCmd() *cobra.Command {
	var language string
	c := &cobra.Command{
		Use:   "attach [NAME] [-- COMMAND...]",
		Short: "Start an STT-managed terminal stream and select it",
		Args: func(cmd *cobra.Command, args []string) error {
			_, _, err := parseAttachArgs(args, cmd.ArgsLenAtDash())
			return err
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			streamName, command, err := parseAttachArgs(args, cmd.ArgsLenAtDash())
			if err != nil {
				return err
			}
			normalizedLanguage, err := ipc.NormalizeLanguage(language)
			if err != nil {
				return err
			}
			return runAttachedTerminal(cmd.Context(), streamName, normalizedLanguage, command)
		},
	}
	c.Flags().StringVar(&language, "language", "", "stream transcription language: auto, en, es, or a locale like es-ES")
	return c
}

func parseAttachArgs(args []string, dash int) (string, []string, error) {
	if dash >= 0 {
		if dash > 1 {
			return "", nil, fmt.Errorf("attach accepts at most one stream name before --")
		}
		if dash == 0 {
			return "", args, nil
		}
		return args[0], args[1:], nil
	}
	if len(args) == 0 {
		return "", nil, nil
	}
	if len(args) > 1 {
		return "", nil, fmt.Errorf("attach commands must follow --")
	}
	return args[0], args[1:], nil
}

func detachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detach [NAME|SLOT|all]",
		Short: "Remove a stream attachment",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &ipc.DetachRequest{}
			if len(args) == 1 {
				if args[0] == "all" {
					req.Name = args[0]
				} else if slot, ok, err := parseSlotArg(args[0]); err != nil {
					return err
				} else if ok {
					req.Slot = slot
				} else {
					req.Name = args[0]
				}
			}
			if req.Name == "" && req.Slot == 0 {
				return fmt.Errorf("stream name or slot is required")
			}
			cli := ipc.NewClient(config.SocketPath())
			if err := cli.Detach(req); err != nil {
				return err
			}
			fmt.Println("detached")
			return nil
		},
	}
}

func parseSlotArg(arg string) (int, bool, error) {
	if arg == "" {
		return 0, false, nil
	}
	for _, r := range arg {
		if r < '0' || r > '9' {
			return 0, false, nil
		}
	}
	slot, err := strconv.Atoi(arg)
	if err != nil {
		return 0, false, err
	}
	if slot < 1 || slot > 9 {
		return 0, false, fmt.Errorf("slot must be between 1 and 9")
	}
	return slot, true, nil
}

func streamCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "stream",
		Short: "Manage STT stream metadata",
	}
	c.AddCommand(streamSetSlotCmd(), streamClearSlotCmd())
	return c
}

func streamSetSlotCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-slot NAME SLOT",
		Short: "Assign a stream to a number hotkey slot (1-9)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			slot, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("slot must be a number from 1 to 9")
			}
			cli := ipc.NewClient(config.SocketPath())
			resp, err := cli.SetSlot(args[0], slot)
			if err != nil {
				return err
			}
			fmt.Printf("stream %q assigned to slot %d\n", resp.Stream.Name, resp.Stream.Slot)
			return nil
		},
	}
}

func streamClearSlotCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear-slot NAME",
		Short: "Clear a stream's number hotkey slot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := ipc.NewClient(config.SocketPath())
			resp, err := cli.ClearSlot(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("stream %q slot cleared\n", resp.Stream.Name)
			return nil
		},
	}
}

func streamIsActive(s ipc.Stream, active string, activeSlot int) bool {
	if activeSlot > 0 {
		return s.Slot == activeSlot
	}
	return s.Name != "" && s.Name == active
}

func selectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "select NAME",
		Short: "Select the one stream that receives staged dictated text",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := ipc.NewClient(config.SocketPath())
			resp, err := cli.Select(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("active stream: %s\n", resp.Active)
			return nil
		},
	}
}

func cycleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cycle",
		Short: "Rotate selected stream to the next live registered stream",
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := ipc.NewClient(config.SocketPath())
			resp, err := cli.Cycle()
			if err != nil {
				return err
			}
			fmt.Printf("active stream: %s\n", resp.Active)
			return nil
		},
	}
}

func pasteCmd() *cobra.Command {
	var transcriptsDir string
	var separator string
	c := &cobra.Command{
		Use:   "paste [N]",
		Short: "Stage recent transcript text in the selected stream buffer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			count := 1
			if len(args) == 1 {
				n, err := strconv.Atoi(args[0])
				if err != nil || n <= 0 {
					return fmt.Errorf("N must be a positive number")
				}
				count = n
			}

			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			dir := cfg.Storage.TranscriptsDir
			if transcriptsDir != "" {
				dir = config.ExpandPath(transcriptsDir)
			}

			records, err := persist.QueryOwnedHistory(dir, persist.HistoryQuery{Limit: count})
			if err != nil {
				return err
			}
			if len(records) == 0 {
				return fmt.Errorf("no non-empty transcripts found in %s", dir)
			}
			parts := make([]string, 0, len(records))
			for _, rec := range records {
				parts = append(parts, strings.TrimSpace(rec.Transcript))
			}
			text := strings.Join(parts, decodeSeparator(separator))

			resp, err := ipc.NewClient(config.SocketPath()).Paste(text)
			if err != nil {
				return err
			}
			label := "transcript"
			if len(records) != 1 {
				label = "transcripts"
			}
			fmt.Printf("staged %d %s in stream %q\n", len(records), label, resp.Stream)
			return nil
		},
	}
	c.Flags().StringVar(&transcriptsDir, "transcripts-dir", "", "directory to read transcript JSON files from")
	c.Flags().StringVar(&separator, "separator", " ", "text inserted between messages; supports \\n, \\r, and \\t escapes")
	return c
}

func fixCmd() *cobra.Command {
	var from string
	var to string
	var last int
	var separator string
	c := &cobra.Command{
		Use:   "fix --to DEST --last N [--from SOURCE]",
		Short: "Fix recent transcript ownership and stage it in another stream",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return fmt.Errorf("--to is required")
			}
			if last <= 0 {
				return fmt.Errorf("--last must be positive")
			}
			cli := ipc.NewClient(config.SocketPath())
			if from == "" {
				monitor, err := cli.Monitor()
				if err != nil {
					return err
				}
				from = monitor.Active
				if from == "" && monitor.ActiveSlot > 0 {
					from = fmt.Sprintf("slot %d", monitor.ActiveSlot)
				}
				if from == "" {
					return fmt.Errorf("--from omitted but no active stream is selected")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "source stream defaulted to active stream %q\n", from)
			}
			resp, err := cli.Fix(ipc.FixRequest{
				From:      from,
				To:        to,
				Last:      last,
				Separator: decodeSeparator(separator),
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "fixed %d message(s) from %q to %q and staged %d chars\n", len(resp.MessageIDs), resp.From, resp.To, resp.Chars)
			return nil
		},
	}
	c.Flags().StringVar(&from, "from", "", "source stream; defaults visibly to the active stream")
	c.Flags().StringVar(&to, "to", "", "destination stream")
	c.Flags().IntVar(&last, "last", 1, "number of latest delivered messages to fix")
	c.Flags().StringVar(&separator, "separator", " ", "text inserted between messages; supports \\n, \\r, and \\t escapes")
	return c
}

func decodeSeparator(s string) string {
	replacer := strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t")
	return replacer.Replace(s)
}

func monitorCmd() *cobra.Command {
	var interval time.Duration
	var once bool
	var porcelain bool
	var historyLimit int
	var historyStream string
	c := &cobra.Command{
		Use:   "monitor",
		Short: "Show daemon dashboard, streams, selected target, and audio level",
		RunE: func(cmd *cobra.Command, args []string) error {
			if interval <= 0 {
				return fmt.Errorf("interval must be positive")
			}
			if historyLimit < 0 {
				return fmt.Errorf("history-limit must be zero or greater")
			}
			cli := ipc.NewClient(config.SocketPath())
			if porcelain {
				monitor, err := cli.Monitor()
				if err != nil {
					return err
				}
				fmt.Print(renderMonitorPorcelain(monitor))
				return nil
			}
			if once {
				monitor, err := cli.Monitor()
				if err != nil {
					return err
				}
				fmt.Print(renderMonitorSnapshot(monitor, false))
				return nil
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			tick := time.NewTicker(interval)
			defer tick.Stop()

			sessionStart := time.Now()
			history := newMonitorHistory(historyLimit, sessionStart)
			fmt.Print("\033[?25l")
			defer fmt.Print("\033[?25h")
			for {
				query := ipc.MonitorQuery{}
				if history.limit > 0 {
					query.HistorySince = history.nextFrom
					query.HistoryStream = historyStream
					query.HistoryLimit = history.limit
				}
				monitor, err := cli.Monitor(query)
				if err != nil {
					return err
				}
				history.Add(monitor.Transcripts)
				monitor.Transcripts = history.Entries()
				monitor.TranscriptHistoryLoaded = history.limit > 0
				fmt.Print("\033[H\033[J")
				fmt.Print(renderMonitorSnapshotWithOptions(monitor, true, monitorRenderOptions{
					SessionStart: sessionStart,
					Now:          time.Now(),
					MaxLines:     terminalHeight(),
					HistoryLimit: history.limit,
				}))
				select {
				case <-ctx.Done():
					fmt.Println()
					return nil
				case <-tick.C:
				}
			}
		},
	}
	c.Flags().DurationVar(&interval, "interval", 250*time.Millisecond, "refresh interval")
	c.Flags().BoolVar(&once, "once", false, "print one combined snapshot and exit")
	c.Flags().BoolVar(&porcelain, "porcelain", false, "print one stable machine-readable snapshot and exit")
	c.Flags().IntVar(&historyLimit, "history-limit", 0, "session transcript ring size; 0 keeps the live monitor dashboard-only")
	c.Flags().StringVar(&historyStream, "history-stream", "", "only show monitor-session history for a specific stream name")
	return c
}

type monitorHistory struct {
	limit    int
	nextFrom time.Time
	entries  []ipc.TranscriptEntry
	seen     map[string]bool
}

func newMonitorHistory(limit int, startedAt time.Time) *monitorHistory {
	return &monitorHistory{
		limit:    limit,
		nextFrom: startedAt.UTC(),
		seen:     map[string]bool{},
	}
}

func (h *monitorHistory) Add(entries []ipc.TranscriptEntry) {
	if h == nil || h.limit <= 0 {
		return
	}
	for _, entry := range entries {
		id := transcriptEntryID(entry)
		if h.seen[id] {
			continue
		}
		h.seen[id] = true
		h.entries = append(h.entries, entry)
		if !entry.Timestamp.IsZero() && !entry.Timestamp.Before(h.nextFrom) {
			h.nextFrom = entry.Timestamp.UTC()
		}
	}
	if len(h.entries) > h.limit {
		drop := len(h.entries) - h.limit
		for _, entry := range h.entries[:drop] {
			delete(h.seen, transcriptEntryID(entry))
		}
		h.entries = h.entries[drop:]
	}
}

func (h *monitorHistory) Entries() []ipc.TranscriptEntry {
	if h == nil || len(h.entries) == 0 {
		return nil
	}
	out := make([]ipc.TranscriptEntry, len(h.entries))
	copy(out, h.entries)
	return out
}

func transcriptEntryID(entry ipc.TranscriptEntry) string {
	return entry.Timestamp.UTC().Format(time.RFC3339Nano) + "|" + entry.TargetRef + "|" + entry.Stream + "|" + entry.Transcript
}

func terminalHeight() int {
	_, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || height <= 0 {
		return 40
	}
	return height
}

type monitorRenderOptions struct {
	SessionStart time.Time
	Now          time.Time
	MaxLines     int
	HistoryLimit int
}

func renderMonitorSnapshot(monitor *ipc.MonitorResponse, color bool) string {
	return renderMonitorSnapshotWithOptions(monitor, color, monitorRenderOptions{})
}

func renderMonitorPorcelain(monitor *ipc.MonitorResponse) string {
	if monitor == nil {
		return ""
	}
	stats := monitorSessionStats(monitor.Streams, monitor.Transcripts)
	var b strings.Builder
	fmt.Fprintf(&b, "monitor version=1 state=%s pid=%d server_ok=%t active=%s active_slot=%d recording_ms=%d audio_level=%.6f stream_count=%d transcript_count=%d transcript_tokens=%d transcript_audio_ms=%d\n",
		porcelainQuote(monitor.State),
		monitor.PID,
		monitor.ServerOK,
		porcelainQuote(monitor.Active),
		monitor.ActiveSlot,
		monitor.RecordingMs,
		monitor.AudioLevel,
		len(monitor.Streams),
		stats.TotalEntries,
		stats.TotalTokens,
		stats.TotalAudioMs,
	)
	if !monitor.LastTranscriptAt.IsZero() || monitor.LastTranscript != "" {
		fmt.Fprintf(&b, "last_transcript ts=%s text=%s\n",
			porcelainTime(monitor.LastTranscriptAt),
			porcelainQuote(monitor.LastTranscript),
		)
	}
	for i, stream := range monitor.Streams {
		streamStats := stats.ByStream[streamKey(stream)]
		fmt.Fprintf(&b, "stream index=%d active=%t id=%s slot=%d name=%s status=%s target_type=%s target_ref=%s target_label=%s target_tty=%s target_cwd=%s target_pid=%d attached_at=%s last_used_at=%s target_attached_at=%s target_last_seen_at=%s transcript_count=%d transcript_tokens=%d transcript_audio_ms=%d language=%s\n",
			i,
			streamIsActive(stream, monitor.Active, monitor.ActiveSlot),
			porcelainQuote(stream.ID),
			stream.Slot,
			porcelainQuote(stream.Name),
			porcelainQuote(stream.Status),
			porcelainQuote(stream.Target.TargetType),
			porcelainQuote(stream.Target.TargetRef),
			porcelainQuote(stream.Target.Label),
			porcelainQuote(stream.Target.TTY),
			porcelainQuote(stream.Target.CWD),
			stream.Target.PID,
			porcelainTime(stream.AttachedAt),
			porcelainTime(stream.LastUsedAt),
			porcelainTime(stream.Target.AttachedAt),
			porcelainTime(stream.Target.LastSeenAt),
			streamStats.Entries,
			streamStats.Tokens,
			streamStats.AudioMs,
			porcelainQuote(stream.Language),
		)
	}
	for i, job := range monitor.Jobs {
		fmt.Fprintf(&b, "job index=%d capture_id=%s stage=%s age_ms=%d updated_ago_ms=%d audio_path=%s target_stream=%s target_type=%s target_ref=%s retryable=%t error=%s language=%s\n",
			i,
			porcelainQuote(job.CaptureID),
			porcelainQuote(job.Stage),
			job.AgeMs,
			job.UpdatedAgoMs,
			porcelainQuote(job.AudioPath),
			porcelainQuote(job.TargetStream),
			porcelainQuote(job.TargetType),
			porcelainQuote(job.TargetRef),
			job.Retryable,
			porcelainQuote(job.Error),
			porcelainQuote(job.Language),
		)
	}
	for i, entry := range monitor.Transcripts {
		fmt.Fprintf(&b, "transcript index=%d ts=%s stream=%s target_type=%s target_ref=%s mode=%s success=%t audio_ms=%d inference_ms=%d tokens=%d error=%s text=%s message_id=%s owned_stream=%s capture_id=%s stage=%s fixed_from=%s fixed_to=%s language=%s\n",
			i,
			porcelainTime(entry.Timestamp),
			porcelainQuote(entry.Stream),
			porcelainQuote(entry.TargetType),
			porcelainQuote(entry.TargetRef),
			porcelainQuote(entry.Mode),
			entry.Success,
			entry.AudioMs,
			entry.InferenceMs,
			estimateTokens(entry.Transcript),
			porcelainQuote(entry.Error),
			porcelainQuote(entry.Transcript),
			porcelainQuote(entry.MessageID),
			porcelainQuote(entry.OwnedStream),
			porcelainQuote(entry.CaptureID),
			porcelainQuote(entry.Stage),
			porcelainQuote(entry.FixedFrom),
			porcelainQuote(entry.FixedTo),
			porcelainQuote(entry.Language),
		)
	}
	return b.String()
}

func porcelainQuote(s string) string {
	return strconv.Quote(s)
}

func porcelainTime(t time.Time) string {
	if t.IsZero() {
		return `""`
	}
	return strconv.Quote(t.UTC().Format(time.RFC3339Nano))
}

func renderMonitorSnapshotWithOptions(monitor *ipc.MonitorResponse, color bool, opts monitorRenderOptions) string {
	if monitor == nil {
		return ""
	}
	active := monitor.Active
	activeSlot := monitor.ActiveSlot
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	stats := monitorSessionStats(monitor.Streams, monitor.Transcripts)

	server := "down"
	if monitor.ServerOK {
		server = "ok"
	}

	var header strings.Builder
	fmt.Fprintf(&header, "state:         %s\n", monitor.State)
	if monitor.PID > 0 {
		fmt.Fprintf(&header, "daemon pid:    %d\n", monitor.PID)
	}
	fmt.Fprintf(&header, "server:        %s\n", server)
	fmt.Fprintf(&header, "active target: %s\n", formatMonitorTarget(monitor.Streams, active, activeSlot, color))
	fmt.Fprintf(&header, "recording:     %s\n", formatRecordingMs(monitor.RecordingMs))
	fmt.Fprintf(&header, "audio level:   %s %.4f\n", levelMeter(monitor.AudioLevel, 20), monitor.AudioLevel)
	if len(monitor.Jobs) > 0 {
		header.WriteString("jobs:\n")
		for _, job := range monitor.Jobs {
			fmt.Fprintf(&header, "  capture=%s stage=%s age=%s updated=%s target=%s retryable=%t\n",
				compactMonitorText(job.CaptureID, 18),
				job.Stage,
				formatDurationShort(time.Duration(job.AgeMs)*time.Millisecond),
				formatDurationShort(time.Duration(job.UpdatedAgoMs)*time.Millisecond),
				formatMonitorJobTarget(job),
				job.Retryable,
			)
			if job.Error != "" {
				fmt.Fprintf(&header, "    error: %s\n", compactMonitorText(job.Error, 160))
			}
		}
	}
	fmt.Fprintf(&header, "stream count:  %d\n", len(monitor.Streams))
	if !opts.SessionStart.IsZero() {
		fmt.Fprintf(&header, "monitor up:    %s\n", formatDurationShort(now.Sub(opts.SessionStart)))
	}
	fmt.Fprintf(&header, "session:       %d transcripts, ~%d tokens, %s talk time\n", stats.TotalEntries, stats.TotalTokens, formatDurationShort(time.Duration(stats.TotalAudioMs)*time.Millisecond))
	header.WriteString("\nstreams:\n")
	if len(monitor.Streams) == 0 {
		header.WriteString("  none registered. run `stt attach` in a terminal.\n")
	} else {
		for _, stream := range monitor.Streams {
			header.WriteString(formatMonitorStream(stream, active, activeSlot, now, stats.ByStream[streamKey(stream)]))
		}
	}

	var history strings.Builder
	if monitor.TranscriptHistoryError != "" {
		fmt.Fprintf(&history, "\ntranscript history unavailable: %s\n", monitor.TranscriptHistoryError)
	} else if monitor.TranscriptHistoryLoaded && len(monitor.Transcripts) == 0 {
		history.WriteString("\nsession history: none yet\n")
	} else if len(monitor.Transcripts) > 0 {
		history.WriteString("\nsession history:\n")
		grouped, unmatched := transcriptGroups(monitor.Streams, monitor.Transcripts)
		for _, stream := range monitor.Streams {
			entries := grouped[streamKey(stream)]
			if len(entries) == 0 {
				continue
			}
			fmt.Fprintf(&history, "%s\n", formatHistoryGroupLabel(stream))
			for _, entry := range entries {
				history.WriteString(formatTranscriptEntry(entry, "  ", color))
			}
		}
		if len(unmatched) > 0 {
			history.WriteString("unmatched stream\n")
			for _, entry := range unmatched {
				history.WriteString(formatTranscriptEntry(entry, "  ", color))
			}
		}
	}
	if !monitor.TranscriptHistoryLoaded && monitor.LastTranscript != "" {
		label := "last transcript"
		if !monitor.LastTranscriptAt.IsZero() {
			label += " (" + monitor.LastTranscriptAt.Local().Format("15:04:05") + ")"
		}
		fmt.Fprintf(&history, "\n%s:\n  %s\n", label, compactMonitorText(monitor.LastTranscript, 240))
	}
	return joinMonitorSections(header.String(), history.String(), opts)
}

func transcriptGroups(streams []ipc.Stream, entries []ipc.TranscriptEntry) (map[string][]ipc.TranscriptEntry, []ipc.TranscriptEntry) {
	groups := make(map[string][]ipc.TranscriptEntry)
	streamByRef := make(map[string]string)
	streamByName := make(map[string]string)
	for _, stream := range streams {
		key := streamKey(stream)
		if stream.Target.TargetRef != "" {
			streamByRef[stream.Target.TargetRef] = key
		}
		if stream.Name != "" {
			streamByName[stream.Name] = key
		}
	}

	var unmatched []ipc.TranscriptEntry
	for _, entry := range entries {
		key := ""
		if entry.OwnedStream != "" {
			key = streamByName[entry.OwnedStream]
		}
		if key == "" && entry.TargetRef != "" {
			key = streamByRef[entry.TargetRef]
		}
		if key == "" && entry.Stream != "" {
			key = streamByName[entry.Stream]
		}
		if key == "" {
			unmatched = append(unmatched, entry)
			continue
		}
		groups[key] = append(groups[key], entry)
	}
	return groups, unmatched
}

type monitorStats struct {
	TotalEntries int
	TotalTokens  int
	TotalAudioMs int
	ByStream     map[string]monitorStreamStats
}

type monitorStreamStats struct {
	Entries int
	Tokens  int
	AudioMs int
}

func monitorSessionStats(streams []ipc.Stream, entries []ipc.TranscriptEntry) monitorStats {
	stats := monitorStats{ByStream: map[string]monitorStreamStats{}}
	streamByRef := make(map[string]string)
	streamByName := make(map[string]string)
	for _, stream := range streams {
		key := streamKey(stream)
		if stream.Target.TargetRef != "" {
			streamByRef[stream.Target.TargetRef] = key
		}
		if stream.Name != "" {
			streamByName[stream.Name] = key
		}
	}
	for _, entry := range entries {
		tokens := estimateTokens(entry.Transcript)
		stats.TotalEntries++
		stats.TotalTokens += tokens
		stats.TotalAudioMs += entry.AudioMs
		key := ""
		if entry.OwnedStream != "" {
			key = streamByName[entry.OwnedStream]
		}
		if key == "" && entry.TargetRef != "" {
			key = streamByRef[entry.TargetRef]
		}
		if key == "" && entry.Stream != "" {
			key = streamByName[entry.Stream]
		}
		if key == "" {
			continue
		}
		streamStats := stats.ByStream[key]
		streamStats.Entries++
		streamStats.Tokens += tokens
		streamStats.AudioMs += entry.AudioMs
		stats.ByStream[key] = streamStats
	}
	return stats
}

func estimateTokens(text string) int {
	return len(strings.Fields(text))
}

func streamKey(stream ipc.Stream) string {
	if stream.ID != "" {
		return stream.ID
	}
	if stream.Target.TargetRef != "" {
		return stream.Target.TargetRef
	}
	if stream.Name != "" {
		return "name:" + stream.Name
	}
	return fmt.Sprintf("slot:%d", stream.Slot)
}

func formatHistoryGroupLabel(stream ipc.Stream) string {
	slot := "-"
	if stream.Slot > 0 {
		slot = strconv.Itoa(stream.Slot)
	}
	name := stream.Name
	if name == "" {
		name = "-"
	}
	return fmt.Sprintf("slot=%s name=%s", slot, name)
}

func formatTranscriptEntry(entry ipc.TranscriptEntry, indent string, color bool) string {
	status := "ok"
	if !entry.Success || entry.Error != "" {
		status = "failed"
	}
	when := "-"
	if !entry.Timestamp.IsZero() {
		when = entry.Timestamp.Local().Format("15:04:05")
	}
	language := entry.Language
	if language == "" {
		language = "-"
	}
	separator := fmt.Sprintf("%s--- %s %s | ~%d tokens | talk=%s | lang=%s ---\n",
		indent, when, status, estimateTokens(entry.Transcript), formatDurationShort(time.Duration(entry.AudioMs)*time.Millisecond), language)
	if color {
		color := "2"
		if status == "failed" {
			color = "31"
		}
		separator = "\033[" + color + "m" + separator + "\033[0m"
	}
	var b strings.Builder
	b.WriteString(separator)
	text := strings.TrimRight(entry.Transcript, "\r\n")
	if strings.TrimSpace(text) == "" {
		text = "(empty transcript)"
	}
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(&b, "%s%s\n", indent, line)
	}
	if entry.Error != "" {
		fmt.Fprintf(&b, "%serror: %s\n", indent, entry.Error)
	}
	if entry.FixedFrom != "" && entry.FixedTo != "" {
		fmt.Fprintf(&b, "%sfixed: %s -> %s\n", indent, entry.FixedFrom, entry.FixedTo)
	}
	return b.String()
}

func formatMonitorJobTarget(job ipc.JobSnapshot) string {
	if job.TargetStream != "" {
		return job.TargetStream
	}
	if job.TargetRef != "" {
		return job.TargetRef
	}
	return "-"
}

func joinMonitorSections(header, history string, opts monitorRenderOptions) string {
	if opts.MaxLines <= 0 {
		return header + history
	}
	headerLines := splitMonitorLines(header)
	if len(headerLines) >= opts.MaxLines {
		return strings.Join(headerLines[:opts.MaxLines], "\n") + "\n"
	}
	historyLines := splitMonitorLines(history)
	budget := opts.MaxLines - len(headerLines)
	if len(historyLines) > budget {
		hidden := len(historyLines) - budget + 1
		tail := historyLines[len(historyLines)-budget+1:]
		historyLines = append([]string{fmt.Sprintf("... %d older history lines hidden by monitor window ...", hidden)}, tail...)
	}
	if len(historyLines) == 0 {
		return header
	}
	return header + strings.Join(historyLines, "\n") + "\n"
}

func splitMonitorLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func formatMonitorTarget(streams []ipc.Stream, active string, activeSlot int, color bool) string {
	slot := "-"
	name := "(none)"
	if active != "" || activeSlot > 0 {
		name = active
		if name == "" {
			name = "-"
		}
		for _, stream := range streams {
			if activeSlot > 0 && stream.Slot != activeSlot {
				continue
			}
			if activeSlot == 0 && stream.Name != active {
				continue
			}
			if stream.Slot > 0 {
				slot = strconv.Itoa(stream.Slot)
			}
			if stream.Name != "" {
				name = stream.Name
			}
			break
		}
	}
	target := fmt.Sprintf("slot=%s name=%s", slot, name)
	if color && activeSlot > 0 {
		return colorizeSlot(activeSlot, target)
	}
	return target
}

func formatMonitorStream(stream ipc.Stream, active string, activeSlot int, now time.Time, stats monitorStreamStats) string {
	marker := " "
	if streamIsActive(stream, active, activeSlot) {
		marker = "*"
	}
	slot := "-"
	if stream.Slot > 0 {
		slot = strconv.Itoa(stream.Slot)
	}
	name := stream.Name
	if name == "" {
		name = "-"
	}
	target := stream.Target.TargetType
	if target == "" {
		target = "-"
	}
	pid := "-"
	if stream.Target.PID > 0 {
		pid = strconv.Itoa(stream.Target.PID)
	}
	attached := "-"
	if !stream.AttachedAt.IsZero() {
		attached = stream.AttachedAt.Local().Format("15:04:05")
	}
	up := "-"
	if !stream.AttachedAt.IsZero() {
		up = formatDurationShort(now.Sub(stream.AttachedAt))
	}
	language := stream.Language
	if language == "" {
		language = "-"
	}
	return fmt.Sprintf("%s slot=%-2s name=%-20s status=%-8s target=%s pid=%s attached=%s up=%s msgs=%d tokens~%d talk=%s lang=%s\n",
		marker, slot, name, stream.Status, target, pid, attached, up, stats.Entries, stats.Tokens, formatDurationShort(time.Duration(stats.AudioMs)*time.Millisecond), language)
}

func compactMonitorText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func formatRecordingMs(ms int) string {
	if ms <= 0 {
		return "0.0s"
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000.0)
}

func formatDurationShort(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%.1fs", float64(d)/float64(time.Second))
	}
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	minutes := int(d / time.Minute)
	d -= time.Duration(minutes) * time.Minute
	seconds := int(d / time.Second)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

func levelMeter(level float64, width int) string {
	if width <= 0 {
		return ""
	}
	if level < 0 {
		level = 0
	}
	scaled := math.Min(level*12, 1)
	filled := int(math.Round(scaled * float64(width)))
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", width-filled) + "]"
}

func colorizeSlot(slot int, text string) string {
	colors := map[int]string{
		1: "34",
		2: "32",
		3: "33",
		4: "35",
		5: "36",
		6: "31",
		7: "37",
		8: "94",
		9: "92",
	}
	color, ok := colors[slot]
	if !ok {
		return text
	}
	return "\033[" + color + "m" + text + "\033[0m"
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose setup (input group, mic, server, daemon)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor()
		},
	}
}
