package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Hotkey struct {
	Device            string `yaml:"device"`
	TalkKey           string `yaml:"talk_key"`
	CycleKey          string `yaml:"cycle_key"`
	CancelKey         string `yaml:"cancel_key"`
	QueryKey          string `yaml:"query_key"`
	FinalizeKey       string `yaml:"finalize_key"`
	CommandKey        string `yaml:"command_key"`
	HoldThresholdMs   int    `yaml:"hold_threshold_ms"`
	DoubleTapWindowMs int    `yaml:"double_tap_window_ms"`
}

type Audio struct {
	Device     string `yaml:"device"`
	SampleRate int    `yaml:"sample_rate"`
}

type Server struct {
	URL       string `yaml:"url"`
	TimeoutMs int    `yaml:"timeout_ms"`
}

type Storage struct {
	TranscriptsDir string `yaml:"transcripts_dir"`
	RegistryPath   string `yaml:"registry_path"`
}

type UI struct {
	Beeps         bool `yaml:"beeps"`
	Notifications bool `yaml:"notifications"`
}

type CommandMode struct {
	CodexCommand string `yaml:"codex_command"`
	CodexModel   string `yaml:"codex_model,omitempty"`
	TimeoutMs    int    `yaml:"timeout_ms"`
}

type Config struct {
	Hotkey  Hotkey      `yaml:"hotkey"`
	Audio   Audio       `yaml:"audio"`
	Server  Server      `yaml:"server"`
	Storage Storage     `yaml:"storage"`
	UI      UI          `yaml:"ui"`
	Command CommandMode `yaml:"command_mode"`
}

const MinHoldThresholdMs = 1000

func Defaults() *Config {
	home, _ := os.UserHomeDir()
	state := filepath.Join(home, ".local", "state", "stt")
	return &Config{
		Hotkey: Hotkey{
			Device:            "auto",
			TalkKey:           "KEY_RIGHTCTRL",
			CycleKey:          "KEY_RIGHTMETA",
			CancelKey:         "KEY_ESC",
			QueryKey:          "KEY_SLASH",
			FinalizeKey:       "KEY_ENTER",
			CommandKey:        "KEY_M",
			HoldThresholdMs:   MinHoldThresholdMs,
			DoubleTapWindowMs: 300,
		},
		Audio: Audio{
			Device:     "default",
			SampleRate: 16000,
		},
		Server: Server{
			URL:       "http://127.0.0.1:8765",
			TimeoutMs: 5000,
		},
		Storage: Storage{
			TranscriptsDir: filepath.Join(state, "transcripts"),
			RegistryPath:   filepath.Join(state, "registry.json"),
		},
		UI: UI{
			Beeps:         true,
			Notifications: true,
		},
		Command: CommandMode{
			CodexCommand: "codex",
			TimeoutMs:    60000,
		},
	}
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "stt", "config.yml")
}

func LoadDefault() (*Config, error) {
	return Load(DefaultPath())
}

// Load reads YAML from path, layering it over defaults. Missing file → defaults only.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.Normalize()
	cfg.Storage.TranscriptsDir = ExpandPath(cfg.Storage.TranscriptsDir)
	cfg.Storage.RegistryPath = ExpandPath(cfg.Storage.RegistryPath)
	return cfg, nil
}

func (c *Config) Normalize() {
	if c.Hotkey.HoldThresholdMs < MinHoldThresholdMs {
		c.Hotkey.HoldThresholdMs = MinHoldThresholdMs
	}
	if c.Command.CodexCommand == "" {
		c.Command.CodexCommand = "codex"
	}
	if c.Command.TimeoutMs <= 0 {
		c.Command.TimeoutMs = 60000
	}
}

func ExpandPath(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if len(path) >= 2 && path[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

// SocketPath returns the IPC unix socket path.
func SocketPath() string {
	return filepath.Join(RuntimeDir(), "daemon.sock")
}

func RuntimeDir() string {
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
		return filepath.Join(runtime, "stt")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("stt-%d", os.Getuid()))
}

func TargetSocketPath(streamName string, pid int) string {
	return filepath.Join(RuntimeDir(), "targets", fmt.Sprintf("%s-%d.sock", SafeName(streamName), pid))
}

var nonSafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func SafeName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = nonSafeName.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		return "stream"
	}
	if len(name) > 40 {
		name = name[:40]
		name = strings.Trim(name, "-")
	}
	if name == "" {
		return "stream"
	}
	return name
}
