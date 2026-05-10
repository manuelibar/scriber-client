package audio

import (
	"fmt"
	"sync"

	"github.com/gen2brain/malgo"
)

// Capture records 16-bit signed mono PCM at the configured sample rate into an in-memory
// buffer using miniaudio (CGO). Lower latency than shelling out to parec — recording
// callbacks fire directly from the audio backend (PipeWire/Pulse/ALSA on Linux).
//
// One Capture instance is meant to be reused across many Start/Stop cycles. Close once
// at shutdown.
type Capture struct {
	ctx        *malgo.AllocatedContext
	sampleRate uint32

	mu        sync.Mutex
	device    *malgo.Device
	buf       []byte
	capturing bool
}

func New(sampleRate int) (*Capture, error) {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("malgo init context: %w", err)
	}
	return &Capture{
		ctx:        mctx,
		sampleRate: uint32(sampleRate),
	}, nil
}

func (c *Capture) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.device != nil {
		_ = c.device.Stop()
		c.device.Uninit()
		c.device = nil
	}
	if c.ctx != nil {
		_ = c.ctx.Uninit()
		c.ctx.Free()
		c.ctx = nil
	}
}

// Start begins capturing. Buffer cleared on each Start.
func (c *Capture) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.capturing {
		return fmt.Errorf("already capturing")
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	cfg.SampleRate = c.sampleRate

	c.buf = c.buf[:0]

	onRecv := func(_, samples []byte, _ uint32) {
		c.mu.Lock()
		c.buf = append(c.buf, samples...)
		c.mu.Unlock()
	}
	dev, err := malgo.InitDevice(c.ctx.Context, cfg, malgo.DeviceCallbacks{Data: onRecv})
	if err != nil {
		return fmt.Errorf("init capture device: %w", err)
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return fmt.Errorf("start capture device: %w", err)
	}
	c.device = dev
	c.capturing = true
	return nil
}

// Stop ends capture and returns the captured PCM bytes.
func (c *Capture) Stop() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.capturing {
		return nil, fmt.Errorf("not capturing")
	}
	_ = c.device.Stop()
	c.device.Uninit()
	c.device = nil
	c.capturing = false

	out := make([]byte, len(c.buf))
	copy(out, c.buf)
	c.buf = c.buf[:0]
	return out, nil
}

// Discard ends capture and throws away the buffer (used when a tap is reclassified).
func (c *Capture) Discard() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.capturing {
		return nil
	}
	_ = c.device.Stop()
	c.device.Uninit()
	c.device = nil
	c.capturing = false
	c.buf = c.buf[:0]
	return nil
}
