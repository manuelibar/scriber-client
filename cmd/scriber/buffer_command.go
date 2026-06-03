package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"scriber/internal/config"
	"scriber/internal/persist"
)

type bufferCommandEditor interface {
	Edit(ctx context.Context, snapshot persist.BufferSnapshot, commandText string) ([]persist.BufferEntry, string, error)
}

type bufferFinalizer interface {
	Finalize(ctx context.Context, snapshot persist.BufferSnapshot) (string, string, error)
}

type codexBufferCommandEditor struct {
	command   string
	model     string
	timeout   time.Duration
	workDir   string
	runCodex  func(ctx context.Context, workDir string, args []string, stdin []byte) ([]byte, error)
	makeTmp   func() (string, error)
	removeDir func(string) error
}

func newCodexBufferCommandEditor(cfg config.CommandMode) *codexBufferCommandEditor {
	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	command := strings.TrimSpace(cfg.CodexCommand)
	if command == "" {
		command = "codex"
	}
	return &codexBufferCommandEditor{
		command: command,
		model:   strings.TrimSpace(cfg.CodexModel),
		timeout: timeout,
		runCodex: func(ctx context.Context, workDir string, args []string, stdin []byte) ([]byte, error) {
			cmd := exec.CommandContext(ctx, command, args...)
			cmd.Dir = workDir
			cmd.Stdin = bytes.NewReader(stdin)
			return cmd.CombinedOutput()
		},
		makeTmp: func() (string, error) {
			return os.MkdirTemp("", "stt-command-*")
		},
		removeDir: os.RemoveAll,
	}
}

type bufferCommandPromptEntry struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type bufferCommandPrompt struct {
	Stream  string                     `json:"stream"`
	Command string                     `json:"command"`
	Entries []bufferCommandPromptEntry `json:"entries"`
}

type bufferCommandResponse struct {
	Entries     []bufferCommandPromptEntry `json:"entries"`
	Explanation string                     `json:"explanation"`
}

func (e *codexBufferCommandEditor) Edit(ctx context.Context, snapshot persist.BufferSnapshot, commandText string) ([]persist.BufferEntry, string, error) {
	if e == nil {
		return nil, "", fmt.Errorf("command editor is unavailable")
	}
	commandText = strings.TrimSpace(commandText)
	if commandText == "" {
		return nil, "", fmt.Errorf("command transcript is empty")
	}
	if len(snapshot.Entries) == 0 {
		return nil, "", fmt.Errorf("buffer %q is empty", snapshot.Stream)
	}

	prompt, err := buildBufferCommandPrompt(snapshot, commandText)
	if err != nil {
		return nil, "", err
	}
	var response bufferCommandResponse
	if err := e.runStructuredCodex(ctx, "command edit", bufferCommandOutputSchema, prompt, &response); err != nil {
		return nil, "", err
	}
	entries, err := applyBufferCommandResponse(snapshot, response)
	if err != nil {
		return nil, "", err
	}
	return entries, strings.TrimSpace(response.Explanation), nil
}

type bufferFinalizationPromptEntry struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
}

type bufferFinalizationPrompt struct {
	Stream  string                          `json:"stream"`
	Entries []bufferFinalizationPromptEntry `json:"entries"`
}

type bufferFinalizationResponse struct {
	Text        string `json:"text"`
	Explanation string `json:"explanation"`
}

func (e *codexBufferCommandEditor) Finalize(ctx context.Context, snapshot persist.BufferSnapshot) (string, string, error) {
	if e == nil {
		return "", "", fmt.Errorf("buffer finalizer is unavailable")
	}
	if len(snapshot.Entries) == 0 {
		return "", "", fmt.Errorf("buffer %q is empty", snapshot.Stream)
	}
	prompt, err := buildBufferFinalizationPrompt(snapshot)
	if err != nil {
		return "", "", err
	}
	var response bufferFinalizationResponse
	if err := e.runStructuredCodex(ctx, "buffer finalization", bufferFinalizationOutputSchema, prompt, &response); err != nil {
		return "", "", err
	}
	text := strings.TrimSpace(response.Text)
	if text == "" {
		return "", "", fmt.Errorf("codex buffer finalization returned empty text")
	}
	return text, strings.TrimSpace(response.Explanation), nil
}

func (e *codexBufferCommandEditor) runStructuredCodex(ctx context.Context, operation, schema, prompt string, out any) error {
	tmp, err := e.makeTmp()
	if err != nil {
		return err
	}
	defer func() {
		if e.removeDir != nil {
			_ = e.removeDir(tmp)
		}
	}()

	schemaPath := filepath.Join(tmp, "output.schema.json")
	resultPath := filepath.Join(tmp, "result.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0o600); err != nil {
		return fmt.Errorf("write codex %s schema: %w", operation, err)
	}

	args := e.structuredCodexArgs(schemaPath, resultPath)

	runCtx := ctx
	cancel := func() {}
	if e.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, e.timeout)
	}
	defer cancel()
	output, err := e.runCodex(runCtx, e.workDir, args, []byte(prompt))
	if err != nil {
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		return fmt.Errorf("codex %s failed: %w: %s", operation, err, strings.TrimSpace(string(output)))
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		return fmt.Errorf("read codex %s result: %w", operation, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse codex %s result: %w", operation, err)
	}
	return nil
}

func (e *codexBufferCommandEditor) structuredCodexArgs(schemaPath, resultPath string) []string {
	args := []string{
		"exec",
		"--disable", "hooks",
		"-c", `model_reasoning_effort="low"`,
		"--skip-git-repo-check",
		"--ephemeral",
		"--ignore-rules",
		"--sandbox", "read-only",
		"--color", "never",
		"--output-schema", schemaPath,
		"--output-last-message", resultPath,
		"-",
	}
	if e.model != "" {
		args = append([]string{"exec", "--model", e.model}, args[1:]...)
	}
	return args
}

func buildBufferCommandPrompt(snapshot persist.BufferSnapshot, commandText string) (string, error) {
	payload := bufferCommandPrompt{
		Stream:  snapshot.Stream,
		Command: commandText,
	}
	for _, entry := range snapshot.Entries {
		payload.Entries = append(payload.Entries, bufferCommandPromptEntry{
			ID:   entry.ID,
			Text: entry.Text,
		})
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return "You edit one persisted speech-to-text buffer.\n" +
		"Apply the command transcript to the buffer entries.\n" +
		"Rules:\n" +
		"- Return only entries that should remain in the buffer.\n" +
		"- You may edit entry text to fix mistakes.\n" +
		"- You may delete entries by omitting them.\n" +
		"- Do not invent new entry IDs.\n" +
		"- Preserve the original order of remaining entries.\n" +
		"- If the command is ambiguous, make the smallest reasonable edit.\n\n" +
		"Input JSON:\n" + string(data) + "\n", nil
}

func buildBufferFinalizationPrompt(snapshot persist.BufferSnapshot) (string, error) {
	payload := bufferFinalizationPrompt{
		Stream: snapshot.Stream,
	}
	for _, entry := range snapshot.Entries {
		payload.Entries = append(payload.Entries, bufferFinalizationPromptEntry{
			ID:       entry.ID,
			Text:     entry.Text,
			Language: entry.Language,
		})
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return "You finalize one spoken speech-to-text buffer made of chronological checkpoints.\n" +
		"Treat the checkpoints as one unstructured spoken monologue: the speaker may wander between topics, revisit earlier points, clarify prior wording, or correct themselves later.\n" +
		"Interpret the whole buffer together and redact a tidy, consistent, clear version that preserves every detail from what was said. Do not summarize by default.\n" +
		"Rules:\n" +
		"- Return the final text exactly as it should be persisted and later injected into the terminal.\n" +
		"- Do not include a trailing newline unless the dictated content explicitly needs one.\n" +
		"- Correct transcription mistakes, casing, punctuation, spacing, and obvious homophones.\n" +
		"- Reorder or group related points when it improves clarity, but do not omit details, caveats, examples, side notes, or topic changes.\n" +
		"- Later checkpoints may correct, clarify, or revisit earlier checkpoints; apply those corrections instead of preserving correction phrases unless they are clearly intended text.\n" +
		"- Translate only when the speaker's intent or language context clearly calls for translation; otherwise preserve the intended language.\n" +
		"- Preserve code, shell commands, product names, file paths, and quoted text unless the transcript clearly says to change them.\n" +
		"- If the input is ambiguous, make the smallest useful correction.\n" +
		"- Do not include rationale inside text; put a short rationale in explanation.\n\n" +
		"Input JSON:\n" + string(data) + "\n", nil
}

func applyBufferCommandResponse(snapshot persist.BufferSnapshot, response bufferCommandResponse) ([]persist.BufferEntry, error) {
	byID := map[string]persist.BufferEntry{}
	position := map[string]int{}
	for i, entry := range snapshot.Entries {
		if entry.ID == "" {
			return nil, fmt.Errorf("buffer entry id is empty")
		}
		byID[entry.ID] = entry
		position[entry.ID] = i
	}
	seen := map[string]bool{}
	last := -1
	out := make([]persist.BufferEntry, 0, len(response.Entries))
	for _, edited := range response.Entries {
		edited.ID = strings.TrimSpace(edited.ID)
		if edited.ID == "" {
			return nil, fmt.Errorf("command response entry id is empty")
		}
		existing, ok := byID[edited.ID]
		if !ok {
			return nil, fmt.Errorf("command response referenced unknown entry id %q", edited.ID)
		}
		if seen[edited.ID] {
			return nil, fmt.Errorf("command response repeated entry id %q", edited.ID)
		}
		seen[edited.ID] = true
		if pos := position[edited.ID]; pos < last {
			return nil, fmt.Errorf("command response changed entry order")
		} else {
			last = pos
		}
		existing.Text = strings.TrimSpace(edited.Text)
		if existing.Text == "" {
			continue
		}
		out = append(out, existing)
	}
	return out, nil
}

const bufferCommandOutputSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "entries": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "id": { "type": "string" },
          "text": { "type": "string" }
        },
        "required": ["id", "text"]
      }
    },
    "explanation": { "type": "string" }
  },
  "required": ["entries", "explanation"]
}`

const bufferFinalizationOutputSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "text": { "type": "string" },
    "explanation": { "type": "string" }
  },
  "required": ["text", "explanation"]
}`
