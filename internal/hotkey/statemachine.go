package hotkey

import (
	"context"
	"log/slog"
	"time"
)

// State of the talk-key FSM.
type State int

const (
	StateIdle         State = iota
	StatePress1Down         // first key is held; tap-vs-hold not yet decided
	StateHolding            // first key confirmed as hold (push-to-talk)
	StateLockArmed          // first key was a tap; waiting for a second tap
	StatePress2Down         // second key is held; tap-vs-hold not yet decided
	StateLocked             // double-tap confirmed; locked recording
	StateLockedEnding       // terminating tap pressed; waiting for its keyup
)

func (s State) String() string {
	return [...]string{"Idle", "Press1Down", "Holding", "LockArmed", "Press2Down", "Locked", "LockedEnding"}[s]
}

// Action emitted by the FSM, applied by the daemon.
type Action int

const (
	ActionNone Action = iota
	ActionStartCapture
	ActionStopAndSend
	ActionDiscardCapture
)

func (a Action) String() string {
	return [...]string{"None", "StartCapture", "StopAndSend", "DiscardCapture"}[a]
}

// Internal FSM input.
type Input int

const (
	InKeyDown Input = iota
	InKeyUp
	InHoldTimer
	InLockTimer
	InCancel
)

// Decision is a pure-function output of the FSM step. The runner applies it.
type Decision struct {
	NewState     State
	Action       Action
	SetHoldTimer bool
	SetLockTimer bool
	ClearTimers  bool
}

// decide is the pure FSM transition. Unknown (state, input) pairs are no-ops
// (NewState = current state, no action).
func decide(state State, in Input) Decision {
	if in == InCancel {
		if state == StateIdle {
			return Decision{NewState: StateIdle}
		}
		return Decision{NewState: StateIdle, Action: ActionDiscardCapture, ClearTimers: true}
	}

	switch state {
	case StateIdle:
		if in == InKeyDown {
			return Decision{NewState: StatePress1Down, Action: ActionStartCapture, SetHoldTimer: true}
		}
	case StatePress1Down:
		if in == InKeyUp {
			return Decision{NewState: StateLockArmed, ClearTimers: true, SetLockTimer: true}
		}
		if in == InHoldTimer {
			return Decision{NewState: StateHolding}
		}
	case StateHolding:
		if in == InKeyUp {
			return Decision{NewState: StateIdle, Action: ActionStopAndSend, ClearTimers: true}
		}
	case StateLockArmed:
		if in == InLockTimer {
			return Decision{NewState: StateIdle, Action: ActionDiscardCapture}
		}
		if in == InKeyDown {
			return Decision{NewState: StatePress2Down, ClearTimers: true, SetHoldTimer: true}
		}
	case StatePress2Down:
		if in == InKeyUp {
			return Decision{NewState: StateLocked, ClearTimers: true}
		}
		if in == InHoldTimer {
			return Decision{NewState: StateHolding}
		}
	case StateLocked:
		if in == InKeyDown {
			return Decision{NewState: StateLockedEnding, Action: ActionStopAndSend}
		}
	case StateLockedEnding:
		if in == InKeyUp {
			return Decision{NewState: StateIdle}
		}
	}
	return Decision{NewState: state}
}

// Config tunes the FSM timings.
type FSMConfig struct {
	HoldThreshold   time.Duration
	DoubleTapWindow time.Duration
}

// Run drives the FSM by consuming key events and emitting Actions on `out`.
// Caller is responsible for closing `events` to stop the runner (or cancelling ctx).
func Run(ctx context.Context, cfg FSMConfig, events <-chan Event, out chan<- Action) {
	state := StateIdle

	var holdTimer, lockTimer *time.Timer
	var holdC, lockC <-chan time.Time

	stop := func(t **time.Timer, c *<-chan time.Time) {
		if *t != nil {
			(*t).Stop()
			*t = nil
		}
		*c = nil
	}

	apply := func(d Decision) {
		if d.ClearTimers {
			stop(&holdTimer, &holdC)
			stop(&lockTimer, &lockC)
		}
		if d.SetHoldTimer {
			holdTimer = time.NewTimer(cfg.HoldThreshold)
			holdC = holdTimer.C
		}
		if d.SetLockTimer {
			lockTimer = time.NewTimer(cfg.DoubleTapWindow)
			lockC = lockTimer.C
		}
		if d.NewState != state {
			slog.Debug("fsm transition", "from", state, "to", d.NewState, "action", d.Action)
		}
		state = d.NewState
		if d.Action != ActionNone {
			select {
			case out <- d.Action:
			case <-ctx.Done():
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev.Kind {
			case KeyDown:
				apply(decide(state, InKeyDown))
			case KeyUp:
				apply(decide(state, InKeyUp))
			case Cancel:
				apply(decide(state, InCancel))
			}
		case <-holdC:
			apply(decide(state, InHoldTimer))
		case <-lockC:
			apply(decide(state, InLockTimer))
		}
	}
}
