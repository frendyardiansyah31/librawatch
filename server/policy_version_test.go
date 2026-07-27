package main

import "testing"

func TestBumpPolicyVersion(t *testing.T) {
	// Arrange
	db := openTestDB(t)

	// Act / Assert — never bumped yet.
	v, err := db.GetPolicyVersion()
	if err != nil {
		t.Fatalf("GetPolicyVersion: %v", err)
	}
	if v != 0 {
		t.Errorf("initial GetPolicyVersion() = %d, want 0", v)
	}

	// Act — bump three times.
	for i, want := range []int64{1, 2, 3} {
		got, err := db.BumpPolicyVersion()
		if err != nil {
			t.Fatalf("BumpPolicyVersion() call %d: %v", i+1, err)
		}
		if got != want {
			t.Errorf("BumpPolicyVersion() call %d = %d, want %d", i+1, got, want)
		}
	}

	// Assert — GetPolicyVersion reflects the last bump.
	final, err := db.GetPolicyVersion()
	if err != nil {
		t.Fatalf("GetPolicyVersion: %v", err)
	}
	if final != 3 {
		t.Errorf("final GetPolicyVersion() = %d, want 3", final)
	}
}
