package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "stt",
		Short:         "Terminal-first speech-to-text with named output streams",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		startCmd(),
		shutdownCmd(),
		daemonCmd(),
		attachCmd(),
		detachCmd(),
		streamCmd(),
		selectCmd(),
		cycleCmd(),
		pasteCmd(),
		historyCmd(),
		monitorCmd(),
		doctorCmd(),
	)
	return root
}

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
