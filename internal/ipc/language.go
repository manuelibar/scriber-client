package ipc

import (
	"fmt"
	"strings"
)

func NormalizeLanguage(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "", nil
	}
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, ":", "-")
	if value == "auto" {
		return "auto", nil
	}
	primary := strings.SplitN(value, "-", 2)[0]
	if len(primary) < 2 || len(primary) > 3 {
		return "", fmt.Errorf("language must be auto, a Whisper language code, or a locale like es-ES")
	}
	for _, r := range primary {
		if r < 'a' || r > 'z' {
			return "", fmt.Errorf("language must be auto, a Whisper language code, or a locale like es-ES")
		}
	}
	return primary, nil
}
