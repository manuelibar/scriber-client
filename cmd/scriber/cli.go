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
			return runAttachedTerminal(cmd.Context(), streamName, command)
		},
	}
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
		Short: "Select the one stream that receives final dictated text",
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
		Short: "Paste recent transcript text into the selected stream",
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

			records, err := persist.LatestNonEmpty(dir, count)
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
			fmt.Printf("pasted %d %s to stream %q\n", len(records), label, resp.Stream)
			return nil
		},
	}
	c.Flags().StringVar(&transcriptsDir, "transcripts-dir", "", "directory to read transcript JSON files from")
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
	var historyLimit int
	var historyStream string
	var legacyTranscripts string
	c := &cobra.Command{
		Use:   "monitor",
		Short: "Show daemon state, streams, selected target, session history, and audio level",
		RunE: func(cmd *cobra.Command, args []string) error {
			if interval <= 0 {
				return fmt.Errorf("interval must be positive")
			}
			if cmd.Flags().Changed("transcripts") {
				n, err := parseMonitorHistoryLimit(legacyTranscripts)
				if err != nil {
					return err
				}
				historyLimit = n
			}
			if historyLimit < 0 {
				return fmt.Errorf("history-limit must be zero or greater")
			}
			cli := ipc.NewClient(config.SocketPath())
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
				fmt.Print("\033[H\033[2J")
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
	c.Flags().IntVar(&historyLimit, "history-limit", 200, "session transcript ring size; 0 hides monitor-session history")
	c.Flags().StringVar(&historyStream, "history-stream", "", "only show monitor-session history for a specific stream name")
	c.Flags().StringVar(&legacyTranscripts, "transcripts", "", "deprecated alias for --history-limit")
	_ = c.Flags().MarkHidden("transcripts")
	return c
}

func parseMonitorHistoryLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if raw == "all" {
		return 200, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("history-limit must be zero or greater")
	}
	return n, nil
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
		if entry.TargetRef != "" {
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
		if entry.TargetRef != "" {
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
	separator := fmt.Sprintf("%s--- %s %s | ~%d tokens | talk=%s ---\n",
		indent, when, status, estimateTokens(entry.Transcript), formatDurationShort(time.Duration(entry.AudioMs)*time.Millisecond))
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
	return b.String()
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
	return fmt.Sprintf("%s slot=%-2s name=%-20s status=%-8s target=%s pid=%s attached=%s up=%s msgs=%d tokens~%d talk=%s\n",
		marker, slot, name, stream.Status, target, pid, attached, up, stats.Entries, stats.Tokens, formatDurationShort(time.Duration(stats.AudioMs)*time.Millisecond))
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
