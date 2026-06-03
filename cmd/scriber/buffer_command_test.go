package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"scriber/internal/persist"
)

func TestApplyBufferCommandResponseEditsAndDeletesEntries(t *testing.T) {
	at := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	snapshot := persist.BufferSnapshot{
		Stream: "codex",
		Entries: []persist.BufferEntry{
			{ID: "one", Stream: "codex", Text: "first", StagedAt: at, TranscriptAt: at},
			{ID: "two", Stream: "codex", Text: "second", StagedAt: at, TranscriptAt: at},
		},
	}

	got, err := applyBufferCommandResponse(snapshot, bufferCommandResponse{
		Entries: []bufferCommandPromptEntry{
			{ID: "one", Text: "first edited"},
		},
	})
	if err != nil {
		t.Fatalf("applyBufferCommandResponse() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "one" || got[0].Text != "first edited" || got[0].StagedAt != at {
		t.Fatalf("edited entries = %+v, want edited first entry with metadata preserved", got)
	}
}

func TestApplyBufferCommandResponseRejectsInvalidIDsAndOrder(t *testing.T) {
	snapshot := persist.BufferSnapshot{
		Stream: "codex",
		Entries: []persist.BufferEntry{
			{ID: "one", Stream: "codex", Text: "first"},
			{ID: "two", Stream: "codex", Text: "second"},
		},
	}

	if _, err := applyBufferCommandResponse(snapshot, bufferCommandResponse{
		Entries: []bufferCommandPromptEntry{{ID: "missing", Text: "x"}},
	}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown id error = %v, want unknown id error", err)
	}
	if _, err := applyBufferCommandResponse(snapshot, bufferCommandResponse{
		Entries: []bufferCommandPromptEntry{{ID: "two", Text: "second"}, {ID: "one", Text: "first"}},
	}); err == nil || !strings.Contains(err.Error(), "order") {
		t.Fatalf("reordered id error = %v, want order error", err)
	}
}

func TestBuildBufferCommandPromptIncludesCommandAndEntries(t *testing.T) {
	prompt, err := buildBufferCommandPrompt(persist.BufferSnapshot{
		Stream:  "codex",
		Entries: []persist.BufferEntry{{ID: "one", Text: "hello"}},
	}, "delete hello")
	if err != nil {
		t.Fatalf("buildBufferCommandPrompt() error = %v", err)
	}
	for _, want := range []string{`"stream": "codex"`, `"command": "delete hello"`, `"id": "one"`, `"text": "hello"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildBufferFinalizationPromptIncludesLanguageAndEntries(t *testing.T) {
	prompt, err := buildBufferFinalizationPrompt(persist.BufferSnapshot{
		Stream: "messages",
		Entries: []persist.BufferEntry{
			{ID: "one", Text: "ola mundo", Language: "es"},
		},
	})
	if err != nil {
		t.Fatalf("buildBufferFinalizationPrompt() error = %v", err)
	}
	for _, want := range []string{
		`"stream": "messages"`,
		`"id": "one"`,
		`"text": "ola mundo"`,
		`"language": "es"`,
		"unstructured spoken monologue",
		"preserves every detail",
		"Do not summarize",
		"Translate only when",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("finalization prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRunStructuredCodexUsesCurrentExecFlags(t *testing.T) {
	tmp := t.TempDir()
	var gotArgs []string
	var gotPrompt string
	editor := &codexBufferCommandEditor{
		model:     "gpt-test",
		timeout:   time.Second,
		makeTmp:   func() (string, error) { return tmp, nil },
		removeDir: func(string) error { return nil },
		runCodex: func(_ context.Context, _ string, args []string, stdin []byte) ([]byte, error) {
			gotArgs = append([]string(nil), args...)
			gotPrompt = string(stdin)
			resultPath := ""
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "--ask-for-approval" {
					t.Fatalf("codex args contain obsolete --ask-for-approval: %v", args)
				}
				if args[i] == "--output-last-message" {
					resultPath = args[i+1]
				}
			}
			if resultPath == "" {
				t.Fatalf("codex args missing --output-last-message: %v", args)
			}
			if err := os.WriteFile(resultPath, []byte(`{"text":"Hello","explanation":"cleaned"}`), 0o600); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}

	var response bufferFinalizationResponse
	if err := editor.runStructuredCodex(context.Background(), "buffer finalization", bufferFinalizationOutputSchema, "finalize me", &response); err != nil {
		t.Fatalf("runStructuredCodex() error = %v", err)
	}
	if response.Text != "Hello" || response.Explanation != "cleaned" {
		t.Fatalf("response = %+v, want parsed codex result", response)
	}
	if gotPrompt != "finalize me" {
		t.Fatalf("stdin prompt = %q, want original prompt", gotPrompt)
	}
	for _, want := range []string{
		"exec",
		"--model", "gpt-test",
		"--disable", "hooks",
		"-c", `model_reasoning_effort="low"`,
		"--skip-git-repo-check",
		"--ephemeral",
		"--ignore-rules",
		"--sandbox", "read-only",
		"--color", "never",
		"--output-schema",
		"--output-last-message",
		"-",
	} {
		if !containsArg(gotArgs, want) {
			t.Fatalf("codex args missing %q: %v", want, gotArgs)
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
