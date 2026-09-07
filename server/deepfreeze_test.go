package main

import "testing"

func TestParseDeepFreezeOutput(t *testing.T) {
	cases := []struct {
		name         string
		resultStatus string
		output       string
		wantStatus   string
		wantDetail   string
	}{
		{"frozen", "ok", "FROZEN", "frozen", ""},
		{"thawed lowercase padded", "ok", "  thawed \n", "thawed", ""},
		{"agent error", "error", "DFC.exe tidak ditemukan", "error", "DFC.exe tidak ditemukan"},
		{"unrecognised output", "ok", "garbage", "unknown", "garbage"},
		{"empty ok output", "ok", "", "unknown", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, gotDetail := parseDeepFreezeOutput(tc.resultStatus, tc.output)
			if gotStatus != tc.wantStatus || gotDetail != tc.wantDetail {
				t.Fatalf("parseDeepFreezeOutput(%q, %q) = (%q, %q), want (%q, %q)",
					tc.resultStatus, tc.output, gotStatus, gotDetail, tc.wantStatus, tc.wantDetail)
			}
		})
	}
}

func TestDeepFreezeActionsMapping(t *testing.T) {
	want := map[string]string{"freeze": "freeze", "thaw": "thaw", "status": "query_df"}
	if len(deepFreezeActions) != len(want) {
		t.Fatalf("deepFreezeActions has %d entries, want %d", len(deepFreezeActions), len(want))
	}
	for verb, payload := range want {
		if got := deepFreezeActions[verb]; got != payload {
			t.Errorf("deepFreezeActions[%q] = %q, want %q", verb, got, payload)
		}
	}
	for _, bad := range []string{"", "reboot", "BOOTFROZEN", "query"} {
		if _, ok := deepFreezeActions[bad]; ok {
			t.Errorf("deepFreezeActions unexpectedly contains %q", bad)
		}
	}
}

func TestDispatchDeepFreezeRejectsBadAction(t *testing.T) {
	// Action validation runs before any *Deployer call, so nil args are safe
	// for the rejection path.
	for _, bad := range []string{"", "reboot", "status", "BOOTFROZEN"} {
		if _, err := dispatchDeepFreeze(nil, nil, "agent-1", bad, "", "test"); err == nil {
			t.Errorf("dispatchDeepFreeze accepted invalid jobAction %q, want error", bad)
		}
	}
}
