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

	"scriber/internal/config"
	"scriber/internal/ipc"
)

func attachCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "attach NAME [-- COMMAND...]",
		Short: "Start an STT-managed terminal stream and select it",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttachedTerminal(cmd.Context(), args[0], args[1:])
		},
	}
	return c
}

func detachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detach [NAME|all]",
		Short: "Remove a stream attachment",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &ipc.DetachRequest{}
			if len(args) == 1 {
				req.Name = args[0]
			}
			if req.Name == "" {
				return fmt.Errorf("stream name is required")
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

func streamsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "streams",
		Short: "Show named STT streams (selected marked with *)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := ipc.NewClient(config.SocketPath())
			resp, err := cli.Streams()
			if err != nil {
				return err
			}
			if len(resp.Streams) == 0 {
				fmt.Println("no streams registered. run `stt attach NAME` in a terminal.")
				return nil
			}
			for _, s := range resp.Streams {
				marker := " "
				if s.Name == resp.Active {
					marker = "*"
				}
				slot := "-"
				if s.Slot > 0 {
					slot = fmt.Sprintf("%d", s.Slot)
				}
				fmt.Printf("%s %-20s slot=%-2s status=%-8s target=%s pid=%d attached=%s\n",
					marker, s.Name, slot, s.Status, s.Target.TargetType, s.Target.PID, s.AttachedAt.Local().Format("15:04:05"))
			}
			return nil
		},
	}
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

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon state, selected stream, and last transcript",
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := ipc.NewClient(config.SocketPath())
			resp, err := cli.Status()
			if err != nil {
				return err
			}
			fmt.Printf("state:         %s\n", resp.State)
			fmt.Printf("active stream: %s\n", resp.Active)
			if resp.ActiveSlot > 0 {
				fmt.Printf("active slot:   %d\n", resp.ActiveSlot)
			}
			if resp.RecordingMs > 0 {
				fmt.Printf("recording:     %.1fs\n", float64(resp.RecordingMs)/1000.0)
			}
			fmt.Printf("audio level:   %s %.4f\n", levelMeter(resp.AudioLevel, 20), resp.AudioLevel)
			fmt.Printf("stream count:  %d\n", resp.StreamCount)
			fmt.Printf("server ok:     %v\n", resp.ServerOK)
			if resp.LastTranscript != "" {
				fmt.Printf("last transcript (%s): %s\n",
					resp.LastTranscriptAt.Local().Format("15:04:05"), resp.LastTranscript)
			}
			return nil
		},
	}
}

func monitorCmd() *cobra.Command {
	var interval time.Duration
	c := &cobra.Command{
		Use:   "monitor",
		Short: "Watch daemon state, selected stream, recording time, and audio level",
		RunE: func(cmd *cobra.Command, args []string) error {
			if interval <= 0 {
				return fmt.Errorf("interval must be positive")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			cli := ipc.NewClient(config.SocketPath())
			tick := time.NewTicker(interval)
			defer tick.Stop()

			for {
				resp, err := cli.Status()
				if err != nil {
					return err
				}
				fmt.Printf("\r\033[K%s", monitorLine(resp))
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
	return c
}

func monitorLine(resp *ipc.StatusResponse) string {
	active := resp.Active
	if active == "" {
		active = "(none)"
	}
	slot := "-"
	if resp.ActiveSlot > 0 {
		slot = strconv.Itoa(resp.ActiveSlot)
		active = colorizeSlot(resp.ActiveSlot, active)
	}
	server := "down"
	if resp.ServerOK {
		server = "ok"
	}
	return fmt.Sprintf(
		"state=%s stream=%s slot=%s server=%s recording=%s level=%s",
		resp.State,
		active,
		slot,
		server,
		formatRecordingMs(resp.RecordingMs),
		levelMeter(resp.AudioLevel, 18),
	)
}

func formatRecordingMs(ms int) string {
	if ms <= 0 {
		return "0.0s"
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000.0)
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
