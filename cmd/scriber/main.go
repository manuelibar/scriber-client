package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:           "scriber",
		Short:         "Push-to-talk dictation that targets a tmux pane",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		daemonCmd(),
		attachCmd(),
		detachCmd(),
		listCmd(),
		switchCmd(),
		cycleCmd(),
		statusCmd(),
		doctorCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
