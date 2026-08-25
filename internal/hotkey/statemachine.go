package hotkey

import (
	"context"
	"log/slog"
	"time"

	"github.com/holoplot/go-evdev"
)

// State of the talk-key gesture recognizer.
type State int

const (
	StateIdle State = iota
	StatePress1Down
	StateMomentaryCapture
	StateTapArmed
	StatePress2Down
	StateLockedCapture
	StateLockedEnding
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "Idle"
	case StatePress1Down:
		return "PendingGesture"
	case StateMomentaryCapture:
		return "MomentaryCapture"
	case StateTapArmed:
		return "TapArmed"
	case StatePress2Down:
		return "SecondTap"
	case StateLockedCapture:
		return "LockedCapture"
	case StateLockedEnding:
		return "LockedEnding"
	default:
		return "Unknown"
	}
}

// Action is the semantic command emitted by the gesture recognizer.
type Action int

const (
	ActionNone Action = iota
	ActionStartMomentaryCapture
	ActionToggleLockedCapture
	ActionFinalizeCapture
	ActionDiscardCapture
	ActionSelectSlot
	ActionReportActiveStream
	ActionCycleStream
)

const (
	ActionStartCapture = ActionStartMomentaryCapture
	ActionStopAndSend  = ActionFinalizeCapture
)

func (a Action) String() string {
	switch a {
	case ActionNone:
		return "None"
	case ActionStartMomentaryCapture:
		return "StartMomentaryCapture"
	case ActionToggleLockedCapture:
		return "ToggleLockedCapture"
	case ActionFinalizeCapture:
		return "FinalizeCapture"
	case ActionDiscardCapture:
		return "DiscardCapture"
	case ActionSelectSlot:
		return "SelectSlot"
	case ActionReportActiveStream:
		return "ReportActiveStream"
	case ActionCycleStream:
		return "CycleStream"
	default:
		return "Unknown"
	}
}

type Command struct {
	Action Action
	Slot   int
	At     time.Time
}

// Internal FSM input for pure transition tests.
type Input int

const (
	InKeyDown Input = iota
	InKeyUp
	InHoldTimer
	InLockTimer
	InCancel
)

type Decision struct {
	NewState      State
	Action        Action
	SetHoldTimer  bool
	SetLockTimer  bool
	ClearTimers   bool
	SuppressKeyUp bool
}

func decide(state State, in Input) Decision {
	if in == InCancel {
		switch state {
		case StateMomentaryCapture, StateLockedCapture:
			return Decision{NewState: StateIdle, Action: ActionDiscardCapture, ClearTimers: true, SuppressKeyUp: true}
		case StatePress1Down, StatePress2Down, StateTapArmed:
			return Decision{NewState: StateIdle, ClearTimers: true, SuppressKeyUp: true}
		default:
			return Decision{NewState: StateIdle}
		}
	}

	switch state {
	case StateIdle:
		if in == InKeyDown {
			return Decision{NewState: StatePress1Down, SetHoldTimer: true}
		}
	case StatePress1Down:
		if in == InKeyUp {
			return Decision{NewState: StateTapArmed, ClearTimers: true, SetLockTimer: true}
		}
		if in == InHoldTimer {
			return Decision{NewState: StateMomentaryCapture, Action: ActionStartMomentaryCapture}
		}
	case StateMomentaryCapture:
		if in == InKeyUp {
			return Decision{NewState: StateIdle, Action: ActionFinalizeCapture, ClearTimers: true}
		}
	case StateTapArmed:
		if in == InLockTimer {
			return Decision{NewState: StateIdle}
		}
		if in == InKeyDown {
			return Decision{NewState: StatePress2Down, ClearTimers: true, SetHoldTimer: true}
		}
	case StatePress2Down:
		if in == InKeyUp {
			return Decision{NewState: StateLockedCapture, Action: ActionToggleLockedCapture, ClearTimers: true}
		}
		if in == InHoldTimer {
			return Decision{NewState: StateLockedCapture, Action: ActionToggleLockedCapture, SuppressKeyUp: true}
		}
	case StateLockedCapture:
		if in == InKeyDown {
			return Decision{NewState: StateLockedEnding, Action: ActionToggleLockedCapture}
		}
	case StateLockedEnding:
		if in == InKeyUp {
			return Decision{NewState: StateIdle}
		}
	}
	return Decision{NewState: state}
}

type FSMConfig struct {
	HoldThreshold   time.Duration
	DoubleTapWindow time.Duration
	TalkKey         evdev.EvCode
	CancelKey       evdev.EvCode
	QueryKey        evdev.EvCode
	CycleKey        evdev.EvCode
	SlotKeys        map[evdev.EvCode]int
}

func DefaultSlotKeys() map[evdev.EvCode]int {
	return map[evdev.EvCode]int{
		evdev.KEY_F1: 1,
		evdev.KEY_F2: 2,
		evdev.KEY_F3: 3,
		evdev.KEY_F4: 4,
		evdev.KEY_F5: 5,
		evdev.KEY_F6: 6,
		evdev.KEY_F7: 7,
		evdev.KEY_F8: 8,
		evdev.KEY_F9: 9,
	}
}

func RunRecognizer(ctx context.Context, cfg FSMConfig, events <-chan Event, out chan<- Command) {
	state := StateIdle
	talkDown := false
	cycleDown := false
	suppressTalkUp := false
	chordHandled := false

	var holdTimer, lockTimer *time.Timer
	var holdC, lockC <-chan time.Time

	stop := func(t **time.Timer, c *<-chan time.Time) {
		if *t != nil {
			(*t).Stop()
			*t = nil
		}
		*c = nil
	}
	clearTimers := func() {
		stop(&holdTimer, &holdC)
		stop(&lockTimer, &lockC)
	}
	emit := func(cmd Command) bool {
		if cmd.At.IsZero() {
			cmd.At = time.Now()
		}
		select {
		case out <- cmd:
			return true
		case <-ctx.Done():
			return false
		}
	}
	apply := func(d Decision, at time.Time) bool {
		if d.ClearTimers {
			clearTimers()
		}
		if d.SetHoldTimer {
			stop(&holdTimer, &holdC)
			holdTimer = time.NewTimer(cfg.HoldThreshold)
			holdC = holdTimer.C
		}
		if d.SetLockTimer {
			stop(&lockTimer, &lockC)
			lockTimer = time.NewTimer(cfg.DoubleTapWindow)
			lockC = lockTimer.C
		}
		if d.SuppressKeyUp {
			suppressTalkUp = true
		}
		if d.NewState != state {
			slog.Debug("gesture transition", "from", state, "to", d.NewState, "action", d.Action)
		}
		state = d.NewState
		if d.Action != ActionNone {
			return emit(Command{Action: d.Action, At: at})
		}
		return true
	}
	emitChord := func(action Action, slot int, at time.Time) bool {
		chordHandled = true
		suppressTalkUp = true
		clearTimers()
		state = StateIdle
		return emit(Command{Action: action, Slot: slot, At: at})
	}

	for {
		select {
		case <-ctx.Done():
			clearTimers()
			return
		case ev, ok := <-events:
			if !ok {
				clearTimers()
				return
			}
			if ev.Kind == Cancel {
				if !apply(decide(state, InCancel), ev.At) {
					return
				}
				continue
			}
			if cfg.CycleKey != 0 && ev.Code == cfg.CycleKey {
				if ev.Kind == KeyDown && !cycleDown {
					cycleDown = true
					if !emit(Command{Action: ActionCycleStream, At: ev.At}) {
						return
					}
				}
				if ev.Kind == KeyUp {
					cycleDown = false
				}
				continue
			}
			if ev.Code == cfg.CancelKey && ev.Kind == KeyDown {
				// In locked capture the talk key is not held, so a bare ESC
				// (common in editors, browsers, dialogs) would silently discard
				// minutes of audio.  Require talk+ESC to cancel.
				if state == StateLockedCapture && !talkDown {
					continue
				}
				if !apply(decide(state, InCancel), ev.At) {
					return
				}
				continue
			}
			if ev.Code == cfg.QueryKey && ev.Kind == KeyDown && talkDown {
				if !chordHandled && state == StatePress1Down {
					if !emitChord(ActionReportActiveStream, 0, ev.At) {
						return
					}
				}
				continue
			}
			if slot, ok := cfg.SlotKeys[ev.Code]; ok && ev.Kind == KeyDown && talkDown {
				if !chordHandled && state == StatePress1Down {
					if !emitChord(ActionSelectSlot, slot, ev.At) {
						return
					}
				}
				continue
			}
			if ev.Code != cfg.TalkKey && cfg.TalkKey != 0 {
				continue
			}
			switch ev.Kind {
			case KeyDown:
				if talkDown && state != StateLockedCapture {
					continue
				}
				talkDown = true
				chordHandled = false
				if !apply(decide(state, InKeyDown), ev.At) {
					return
				}
			case KeyUp:
				if !talkDown {
					continue
				}
				talkDown = false
				chordHandled = false
				if suppressTalkUp {
					suppressTalkUp = false
					continue
				}
				if !apply(decide(state, InKeyUp), ev.At) {
					return
				}
			}
		case <-holdC:
			if !apply(decide(state, InHoldTimer), time.Now()) {
				return
			}
		case <-lockC:
			if !apply(decide(state, InLockTimer), time.Now()) {
				return
			}
		}
	}
}

func Run(ctx context.Context, cfg FSMConfig, events <-chan Event, out chan<- Action) {
	commands := make(chan Command, 8)
	go func() {
		RunRecognizer(ctx, cfg, events, commands)
		close(commands)
	}()
	for cmd := range commands {
		switch cmd.Action {
		case ActionStartMomentaryCapture, ActionToggleLockedCapture, ActionFinalizeCapture, ActionDiscardCapture:
			select {
			case out <- cmd.Action:
			case <-ctx.Done():
				return
			}
		}
	}
}
