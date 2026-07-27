package main

import (
	"testing"
	"time"
)

// fakeKillDeps builds a killDeps for verifyAndKillPID's tests — pidExists
// returns true for the first alwaysAliveFor calls, then false, simulating
// the process actually dying after some number of verify checks. sleep is a
// no-op so tests run instantly instead of waiting real wall-clock time.
func fakeKillDeps(alwaysAliveFor int) (*killDeps, *int) {
	checkCount := 0
	terminateCalls := 0
	gracefulCalls := 0
	deps := &killDeps{
		terminate: func() (string, error) {
			terminateCalls++
			return "", nil
		},
		pidExists: func() bool {
			checkCount++
			return checkCount <= alwaysAliveFor
		},
		gracefulClose: func() { gracefulCalls++ },
		sleep:         func(time.Duration) {},
	}
	return deps, &terminateCalls
}

func TestVerifyAndKillPID_AlreadyExited(t *testing.T) {
	// Arrange — pidExists returns false immediately.
	deps, terminateCalls := fakeKillDeps(0)

	// Act
	result := verifyAndKillPID(*deps)

	// Assert
	if !result.Success {
		t.Errorf("Success = false, want true (process was already gone)")
	}
	if result.Reason != KillReasonAlreadyExited {
		t.Errorf("Reason = %q, want %q", result.Reason, KillReasonAlreadyExited)
	}
	if *terminateCalls != 0 {
		t.Errorf("terminate was called %d times, want 0 (no kill needed)", *terminateCalls)
	}
}

func TestVerifyAndKillPID_SucceedsOnFirstAttempt(t *testing.T) {
	// Arrange — alive for the initial check, then gone after the first
	// terminate+verify.
	deps, terminateCalls := fakeKillDeps(1)

	// Act
	result := verifyAndKillPID(*deps)

	// Assert
	if !result.Success {
		t.Error("Success = false, want true")
	}
	if result.Reason != "" {
		t.Errorf("Reason = %q, want empty on success", result.Reason)
	}
	if *terminateCalls != 1 {
		t.Errorf("terminate was called %d times, want 1", *terminateCalls)
	}
}

func TestVerifyAndKillPID_SucceedsAfterGracefulClose(t *testing.T) {
	// Arrange — survives the initial check plus 2 forced attempts (3 alive
	// checks total before those attempts), then finally dies right after
	// the graceful-close step's verify.
	//
	// Sequence of pidExists calls: initial(alive) -> after attempt1(alive)
	// -> after attempt2(alive) -> after gracefulClose(dead).
	deps, terminateCalls := fakeKillDeps(3)
	var gracefulCalled bool
	deps.gracefulClose = func() { gracefulCalled = true }

	// Act
	result := verifyAndKillPID(*deps)

	// Assert
	if !result.Success {
		t.Error("Success = false, want true")
	}
	if !gracefulCalled {
		t.Error("gracefulClose was never called, want it invoked after forceAttempts are exhausted")
	}
	if *terminateCalls != 2 {
		t.Errorf("terminate was called %d times before graceful-close, want 2 (forceAttempts)", *terminateCalls)
	}
}

func TestVerifyAndKillPID_FailsAfterEveryStep(t *testing.T) {
	// Arrange — never dies, no matter how many checks.
	deps, terminateCalls := fakeKillDeps(1000)
	baseTerminate := deps.terminate
	deps.terminate = func() (string, error) {
		_, _ = baseTerminate() // keep terminateCalls accounting from fakeKillDeps
		return "ERROR: Access is denied.", nil
	}

	// Act
	result := verifyAndKillPID(*deps)

	// Assert
	if result.Success {
		t.Error("Success = true, want false (process never actually died)")
	}
	if result.Reason != KillReasonAccessDenied {
		t.Errorf("Reason = %q, want %q", result.Reason, KillReasonAccessDenied)
	}
	// 2 forceAttempts + 1 final attempt after graceful-close = 3 total.
	if *terminateCalls != 3 {
		t.Errorf("terminate was called %d times, want 3 (2 forceAttempts + 1 final)", *terminateCalls)
	}
}

func TestClassifyKillFailure(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "access denied", output: "ERROR: Access is denied.", want: KillReasonAccessDenied},
		{name: "access denied lowercase", output: "access denied", want: KillReasonAccessDenied},
		{name: "empty output", output: "", want: KillReasonUnknown},
		{name: "unrecognized output", output: "something unexpected happened", want: KillReasonVerificationFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			got := classifyKillFailure(tc.output)

			// Assert
			if got != tc.want {
				t.Errorf("classifyKillFailure(%q) = %q, want %q", tc.output, got, tc.want)
			}
		})
	}
}
