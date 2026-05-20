package audio

import (
	"math"
	"testing"
)

func TestRMSPCM16(t *testing.T) {
	tests := []struct {
		name string
		pcm  []byte
		want float64
	}{
		{name: "empty", pcm: nil, want: 0},
		{name: "silence", pcm: []byte{0x00, 0x00, 0x00, 0x00}, want: 0},
		{name: "full scale positive", pcm: []byte{0xff, 0x7f}, want: float64(32767) / 32768.0},
		{name: "full scale negative", pcm: []byte{0x00, 0x80}, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RMSPCM16(tt.pcm)
			if math.Abs(got-tt.want) > 0.000001 {
				t.Fatalf("RMSPCM16() = %.8f, want %.8f", got, tt.want)
			}
		})
	}
}
