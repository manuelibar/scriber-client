package hotkey

import (
	"testing"
)

func TestDecide_Transitions(t *testing.T) {
	tests := []struct {
		name  string
		state State
		in    Input
		want  Decision
	}{
		// Push-to-talk happy path
		{"idle + keydown -> press1+capture+hold-timer",
			StateIdle, InKeyDown,
			Decision{NewState: StatePress1Down, Action: ActionStartCapture, SetHoldTimer: true}},
		{"press1 + hold-timer -> holding",
			StatePress1Down, InHoldTimer,
			Decision{NewState: StateHolding}},
		{"holding + keyup -> idle+stop+send",
			StateHolding, InKeyUp,
			Decision{NewState: StateIdle, Action: ActionStopAndSend, ClearTimers: true}},

		// Tap that doesn't become double-tap (single-tap-then-timeout)
		{"press1 + keyup -> lockarmed+lock-timer",
			StatePress1Down, InKeyUp,
			Decision{NewState: StateLockArmed, ClearTimers: true, SetLockTimer: true}},
		{"lockarmed + lock-timer -> idle+discard",
			StateLockArmed, InLockTimer,
			Decision{NewState: StateIdle, Action: ActionDiscardCapture}},

		// Double-tap to lock
		{"lockarmed + keydown -> press2+hold-timer",
			StateLockArmed, InKeyDown,
			Decision{NewState: StatePress2Down, ClearTimers: true, SetHoldTimer: true}},
		{"press2 + keyup -> locked",
			StatePress2Down, InKeyUp,
			Decision{NewState: StateLocked, ClearTimers: true}},
		{"locked + keydown -> ending+stop+send",
			StateLocked, InKeyDown,
			Decision{NewState: StateLockedEnding, Action: ActionStopAndSend}},
		{"ending + keyup -> idle",
			StateLockedEnding, InKeyUp,
			Decision{NewState: StateIdle}},

		// Tap-then-hold: second press is held, abandon lock-arm and treat as PTT
		{"press2 + hold-timer -> holding",
			StatePress2Down, InHoldTimer,
			Decision{NewState: StateHolding}},

		// Ignored inputs (no-op): random subset
		{"idle + keyup ignored",
			StateIdle, InKeyUp,
			Decision{NewState: StateIdle}},
		{"idle + hold-timer ignored",
			StateIdle, InHoldTimer,
			Decision{NewState: StateIdle}},
		{"holding + keydown ignored",
			StateHolding, InKeyDown,
			Decision{NewState: StateHolding}},
		{"locked + keyup ignored",
			StateLocked, InKeyUp,
			Decision{NewState: StateLocked}},

		// External chord handling can cancel an in-progress tap/hold without sending.
		{"press1 + cancel -> idle+discard",
			StatePress1Down, InCancel,
			Decision{NewState: StateIdle, Action: ActionDiscardCapture, ClearTimers: true}},
		{"holding + cancel -> idle+discard",
			StateHolding, InCancel,
			Decision{NewState: StateIdle, Action: ActionDiscardCapture, ClearTimers: true}},
		{"idle + cancel ignored",
			StateIdle, InCancel,
			Decision{NewState: StateIdle}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(tc.state, tc.in)
			if got != tc.want {
				t.Errorf("decide(%s, %d) = %+v, want %+v", tc.state, tc.in, got, tc.want)
			}
		})
	}
}

// Sequence tests follow a flow through multiple decisions and assert the actions emitted.
func TestDecide_Sequences(t *testing.T) {
	type step struct {
		in    Input
		want  Action
		state State // expected state after this step
	}
	scenarios := []struct {
		name  string
		steps []step
	}{
		{
			name: "push-to-talk: keydown, hold, keyup",
			steps: []step{
				{InKeyDown, ActionStartCapture, StatePress1Down},
				{InHoldTimer, ActionNone, StateHolding},
				{InKeyUp, ActionStopAndSend, StateIdle},
			},
		},
		{
			name: "single tap that times out",
			steps: []step{
				{InKeyDown, ActionStartCapture, StatePress1Down},
				{InKeyUp, ActionNone, StateLockArmed},
				{InLockTimer, ActionDiscardCapture, StateIdle},
			},
		},
		{
			name: "double-tap lock then end",
			steps: []step{
				{InKeyDown, ActionStartCapture, StatePress1Down},
				{InKeyUp, ActionNone, StateLockArmed},
				{InKeyDown, ActionNone, StatePress2Down},
				{InKeyUp, ActionNone, StateLocked},
				{InKeyDown, ActionStopAndSend, StateLockedEnding},
				{InKeyUp, ActionNone, StateIdle},
			},
		},
		{
			name: "tap then hold (abandon lock-arm)",
			steps: []step{
				{InKeyDown, ActionStartCapture, StatePress1Down},
				{InKeyUp, ActionNone, StateLockArmed},
				{InKeyDown, ActionNone, StatePress2Down},
				{InHoldTimer, ActionNone, StateHolding},
				{InKeyUp, ActionStopAndSend, StateIdle},
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			state := StateIdle
			for i, st := range sc.steps {
				d := decide(state, st.in)
				if d.Action != st.want {
					t.Errorf("step %d: action = %s, want %s", i, d.Action, st.want)
				}
				if d.NewState != st.state {
					t.Errorf("step %d: state = %s, want %s", i, d.NewState, st.state)
				}
				state = d.NewState
			}
		})
	}
}
