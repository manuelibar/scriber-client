package notify

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var replaceMu sync.Mutex
var replaceID string

type Message struct {
	Title string
	Body  string
}

type Worker struct {
	enabled bool
	ch      chan Message
}

func NewWorker(ctx context.Context, enabled bool, capacity int) *Worker {
	if capacity <= 0 {
		capacity = 1
	}
	w := &Worker{
		enabled: enabled,
		ch:      make(chan Message, capacity),
	}
	go w.run(ctx)
	return w
}

func (w *Worker) Send(title, body string) {
	if w == nil || !w.enabled {
		return
	}
	msg := Message{Title: title, Body: body}
	select {
	case w.ch <- msg:
		return
	default:
	}
	select {
	case <-w.ch:
	default:
	}
	select {
	case w.ch <- msg:
	default:
		slog.Debug("dropping notification", "title", title)
	}
}

func (w *Worker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-w.ch:
			SendContext(ctx, msg.Title, msg.Body, 800*time.Millisecond)
		}
	}
}

// Send emits a desktop notification. Best-effort; logs and ignores errors.
func Send(title, body string) {
	SendContext(context.Background(), title, body, 800*time.Millisecond)
}

func SendContext(ctx context.Context, title, body string, timeout time.Duration) {
	if timeout <= 0 {
		timeout = 800 * time.Millisecond
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	replaceMu.Lock()
	defer replaceMu.Unlock()

	args := notifyArgs(title, body, true)
	out, err := exec.CommandContext(cmdCtx, "notify-send", args...).Output()
	if err != nil && replaceID != "" {
		replaceID = ""
		out, err = exec.CommandContext(cmdCtx, "notify-send", notifyArgs(title, body, true)...).Output()
	}
	if err == nil {
		if id := strings.TrimSpace(string(out)); id != "" {
			replaceID = id
		}
		return
	}

	cmd := exec.CommandContext(cmdCtx, "notify-send", notifyArgs(title, body, false)...)
	if err := cmd.Run(); err != nil {
		slog.Debug("notify-send failed", "err", err)
	}
}

func notifyArgs(title, body string, printID bool) []string {
	args := []string{
		"-a", "scriber",
		"-t", "2500",
		"-e",
		"-h", "string:x-canonical-private-synchronous:scriber",
	}
	if printID {
		args = append(args, "-p")
		if replaceID != "" {
			args = append(args, "-r", replaceID)
		}
	}
	args = append(args, title)
	if body != "" {
		args = append(args, body)
	}
	return args
}
