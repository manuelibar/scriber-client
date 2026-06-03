package ipc

import "testing"

func TestNormalizeLanguage(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "auto", want: "auto"},
		{in: "es", want: "es"},
		{in: "es-ES", want: "es"},
		{in: "en_US", want: "en"},
		{in: "en:US", want: "en"},
		{in: "123", wantErr: true},
	}
	for _, tc := range tests {
		got, err := NormalizeLanguage(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("NormalizeLanguage(%q) error = nil, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("NormalizeLanguage(%q) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeLanguage(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
