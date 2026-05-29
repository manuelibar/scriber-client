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
		{"idle + keydown -> press1+hold-timer",
			StateIdle, InKeyDown,
			Decision{NewState: StatePress1Down, SetHoldTimer: true}},
		{"press1 + hold-timer -> holding+capture",
			StatePress1Down, InHoldTimer,
			Decision{NewState: StateHolding, Action: ActionStartCapture}},
		{"holding + keyup -> idle+stop+send",
			StateHolding, InKeyUp,
			Decision{NewState: StateIdle, Action: ActionStopAndSend, ClearTimers: true}},

		// Tap that doesn't become double-tap (single-tap-then-timeout)
		{"press1 + keyup -> lockarmed+lock-timer",
			StatePress1Down, InKeyUp,
			Decision{NewState: StateLockArmed, ClearTimers: true, SetLockTimer: true}},
		{"lockarmed + lock-timer -> idle",
			StateLockArmed, InLockTimer,
			Decision{NewState: StateIdle}},

		// Double-tap without holding must not start capture.
		{"lockarmed + keydown -> press2+hold-timer",
			StateLockArmed, InKeyDown,
			Decision{NewState: StatePress2Down, ClearTimers: true, SetHoldTimer: true}},
		{"press2 + keyup -> idle",
			StatePress2Down, InKeyUp,
			Decision{NewState: StateIdle, ClearTimers: true}},
		{"press2 + hold-timer -> locked+capture",
			StatePress2Down, InHoldTimer,
			Decision{NewState: StateLocked, Action: ActionStartCapture}},
		{"locked + keydown -> ending+stop+send",
			StateLocked, InKeyDown,
			Decision{NewState: StateLockedEnding, Action: ActionStopAndSend}},
		{"ending + keyup -> idle",
			StateLockedEnding, InKeyUp,
			Decision{NewState: StateIdle}},

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
				{InKeyDown, ActionNone, StatePress1Down},
				{InHoldTimer, ActionStartCapture, StateHolding},
				{InKeyUp, ActionStopAndSend, StateIdle},
			},
		},
		{
			name: "single tap that times out",
			steps: []step{
				{InKeyDown, ActionNone, StatePress1Down},
				{InKeyUp, ActionNone, StateLockArmed},
				{InLockTimer, ActionNone, StateIdle},
			},
		},
		{
			name: "quick double-tap does not start capture",
			steps: []step{
				{InKeyDown, ActionNone, StatePress1Down},
				{InKeyUp, ActionNone, StateLockArmed},
				{InKeyDown, ActionNone, StatePress2Down},
				{InKeyUp, ActionNone, StateIdle},
			},
		},
		{
			name: "tap then hold locks after hold threshold",
			steps: []step{
				{InKeyDown, ActionNone, StatePress1Down},
				{InKeyUp, ActionNone, StateLockArmed},
				{InKeyDown, ActionNone, StatePress2Down},
				{InHoldTimer, ActionStartCapture, StateLocked},
				{InKeyDown, ActionStopAndSend, StateLockedEnding},
				{InKeyUp, ActionNone, StateIdle},
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
