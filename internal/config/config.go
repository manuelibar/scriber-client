package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Hotkey struct {
	Device             string `yaml:"device"`
	TalkKey            string `yaml:"talk_key"`
	CycleKey           string `yaml:"cycle_key"`
	HoldThresholdMs    int    `yaml:"hold_threshold_ms"`
	DoubleTapWindowMs  int    `yaml:"double_tap_window_ms"`
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

type Config struct {
	Hotkey  Hotkey  `yaml:"hotkey"`
	Audio   Audio   `yaml:"audio"`
	Server  Server  `yaml:"server"`
	Storage Storage `yaml:"storage"`
	UI      UI      `yaml:"ui"`
}

func Defaults() *Config {
	home, _ := os.UserHomeDir()
	state := filepath.Join(home, ".local", "state", "scriber")
	return &Config{
		Hotkey: Hotkey{
			Device:            "auto",
			TalkKey:           "KEY_RIGHTCTRL",
			CycleKey:          "KEY_RIGHTMETA",
			HoldThresholdMs:   180,
			DoubleTapWindowMs: 350,
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
	}
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "scriber", "config.yml")
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
	return cfg, nil
}

// SocketPath returns the IPC unix socket path.
func SocketPath() string {
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
		return filepath.Join(runtime, "scriber.sock")
	}
	return fmt.Sprintf("/run/user/%d/scriber.sock", os.Getuid())
}
