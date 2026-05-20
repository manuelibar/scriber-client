package main

import "testing"

func TestRootCommandUsesSTTNaming(t *testing.T) {
	root := newRootCmd()
	if root.Use != "stt" {
		t.Fatalf("root Use = %q, want stt", root.Use)
	}
	if root.Short == "" {
		t.Fatalf("root Short should be STT/stream oriented, got %q", root.Short)
	}
}

func TestRootCommandHasStreamCommands(t *testing.T) {
	root := newRootCmd()
	commands := map[string]bool{}
	for _, cmd := range root.Commands() {
		commands[cmd.Name()] = true
	}
	for _, want := range []string{"attach", "stream", "streams", "select", "cycle", "status", "monitor", "doctor"} {
		if !commands[want] {
			t.Fatalf("root command missing %q; got %v", want, commands)
		}
	}
}

func TestAttachAndSelectUsage(t *testing.T) {
	attach := attachCmd()
	if attach.Use != "attach NAME [-- COMMAND...]" {
		t.Fatalf("attach Use = %q, want attach NAME [-- COMMAND...]", attach.Use)
	}
	selectCmd := selectCmd()
	if selectCmd.Use != "select NAME" {
		t.Fatalf("select Use = %q, want select NAME", selectCmd.Use)
	}
}

func TestStreamSetSlotUsage(t *testing.T) {
	stream := streamCmd()
	commands := map[string]bool{}
	for _, cmd := range stream.Commands() {
		commands[cmd.Name()] = true
	}
	if !commands["set-slot"] || !commands["clear-slot"] {
		t.Fatalf("stream command missing slot subcommands; got %v", commands)
	}
}

func TestLevelMeter(t *testing.T) {
	if got := levelMeter(0, 5); got != "[-----]" {
		t.Fatalf("levelMeter(0, 5) = %q", got)
	}
	if got := levelMeter(1, 5); got != "[#####]" {
		t.Fatalf("levelMeter(1, 5) = %q", got)
	}
}
