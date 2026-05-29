package main

import (
	"os"
	"testing"
)

func TestProcCmdlineArgs(t *testing.T) {
	got := procCmdlineArgs([]byte("/home/me/.local/bin/stt\x00daemon\x00--flag\x00"))
	want := []string{"/home/me/.local/bin/stt", "daemon", "--flag"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
}

func TestIsSTTDaemonCmdline(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{args: []string{"/home/me/.local/bin/stt", "daemon"}, want: true},
		{args: []string{"/tmp/scriber", "daemon"}, want: true},
		{args: []string{"/home/me/.local/bin/stt", "start"}, want: false},
		{args: []string{"/usr/bin/other", "daemon"}, want: false},
		{args: []string{"/home/me/.local/bin/stt"}, want: false},
	}
	for _, tc := range tests {
		if got := isSTTDaemonCmdline(tc.args); got != tc.want {
			t.Fatalf("isSTTDaemonCmdline(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestOtherDaemonProcessesSkipsCurrentAndKeptPID(t *testing.T) {
	self := os.Getpid()
	procs := []daemonProcess{
		{PID: self},
		{PID: 101},
		{PID: 202},
	}
	got := otherDaemonProcesses(procs, 101)
	if len(got) != 1 || got[0].PID != 202 {
		t.Fatalf("otherDaemonProcesses() = %+v, want only pid 202", got)
	}
}
