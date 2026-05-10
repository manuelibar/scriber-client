package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"scriber/internal/config"
	"scriber/internal/ipc"
)

func gatherAttachReq(alias string) *ipc.AttachRequest {
	cwd, _ := os.Getwd()
	tty, _ := os.Readlink("/proc/self/fd/0")
	return &ipc.AttachRequest{
		PID:      os.Getpid(),
		PPID:     os.Getppid(),
		TTY:      tty,
		TMUXPane: os.Getenv("TMUX_PANE"),
		STY:      os.Getenv("STY"),
		CWD:      cwd,
		Term:     os.Getenv("TERM"),
		Alias:    alias,
	}
}

func attachCmd() *cobra.Command {
	var alias string
	c := &cobra.Command{
		Use:   "attach",
		Short: "Register the current $TMUX_PANE as a target",
		RunE: func(cmd *cobra.Command, args []string) error {
			req := gatherAttachReq(alias)
			if req.TMUXPane == "" {
				return fmt.Errorf("not inside tmux: $TMUX_PANE is empty (MVP supports tmux only)")
			}
			cli := ipc.NewClient(config.SocketPath())
			resp, err := cli.Attach(req)
			if err != nil {
				return err
			}
			fmt.Printf("attached pane %s as %q (mode: %s, session: %s)\n",
				resp.Pane.PaneID, resp.Pane.Alias, resp.Pane.Mode, resp.Pane.Session)
			if resp.Message != "" {
				fmt.Println("note:", resp.Message)
			}
			return nil
		},
	}
	c.Flags().StringVar(&alias, "alias", "", "human-readable name (default: <session>-<pane#>)")
	return c
}

func detachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detach [ALIAS|all]",
		Short: "Remove a registered pane (default: current pane)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &ipc.DetachRequest{TMUXPane: os.Getenv("TMUX_PANE")}
			if len(args) == 1 {
				req.Alias = args[0]
				req.TMUXPane = ""
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

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show registered panes (active marked with *)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := ipc.NewClient(config.SocketPath())
			resp, err := cli.List()
			if err != nil {
				return err
			}
			if len(resp.Panes) == 0 {
				fmt.Println("no panes registered. run `scriber attach` inside a tmux pane.")
				return nil
			}
			for _, p := range resp.Panes {
				marker := " "
				if p.Alias == resp.Active {
					marker = "*"
				}
				fmt.Printf("%s %-20s %-6s session=%s attached=%s\n",
					marker, p.Alias, p.PaneID, p.Session, p.AttachedAt.Local().Format("15:04:05"))
			}
			return nil
		},
	}
}

func switchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch ALIAS",
		Short: "Set the active target pane",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := ipc.NewClient(config.SocketPath())
			resp, err := cli.Switch(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("active: %s\n", resp.Active)
			return nil
		},
	}
}

func cycleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cycle",
		Short: "Rotate active pane to the next registered one",
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := ipc.NewClient(config.SocketPath())
			resp, err := cli.Cycle()
			if err != nil {
				return err
			}
			fmt.Printf("active: %s\n", resp.Active)
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon state, last transcript, recent latencies",
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := ipc.NewClient(config.SocketPath())
			resp, err := cli.Status()
			if err != nil {
				return err
			}
			fmt.Printf("state:        %s\n", resp.State)
			fmt.Printf("active pane:  %s\n", resp.Active)
			fmt.Printf("pane count:   %d\n", resp.PaneCount)
			fmt.Printf("server ok:    %v\n", resp.ServerOK)
			if resp.LastTranscript != "" {
				fmt.Printf("last transcript (%s): %s\n",
					resp.LastTranscriptAt.Local().Format("15:04:05"), resp.LastTranscript)
			}
			return nil
		},
	}
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose setup (input group, mic, tmux, server, daemon)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor()
		},
	}
}
