package shared

import "testing"

func TestMatchScore(t *testing.T) {
	catA := int64(1)
	catB := int64(2)

	tests := []struct {
		name      string
		rule      PolicyRule
		ctx       PolicyContext
		wantScore int
		wantOK    bool
	}{
		{
			name:      "empty rule (all any) matches everything with score 0",
			rule:      PolicyRule{},
			ctx:       PolicyContext{AppStatus: "blocked"},
			wantScore: 0,
			wantOK:    true,
		},
		{
			name:      "app_status matches",
			rule:      PolicyRule{AppStatus: "blocked"},
			ctx:       PolicyContext{AppStatus: "blocked"},
			wantScore: 1,
			wantOK:    true,
		},
		{
			name:      "app_status mismatch",
			rule:      PolicyRule{AppStatus: "blocked"},
			ctx:       PolicyContext{AppStatus: "allowed"},
			wantScore: 0,
			wantOK:    false,
		},
		{
			name:      "category_id nil ctx never matches a category-specific rule",
			rule:      PolicyRule{CategoryID: &catA},
			ctx:       PolicyContext{CategoryID: nil},
			wantScore: 0,
			wantOK:    false,
		},
		{
			name:      "category_id mismatch",
			rule:      PolicyRule{CategoryID: &catA},
			ctx:       PolicyContext{CategoryID: &catB},
			wantScore: 0,
			wantOK:    false,
		},
		{
			name:      "execution_location case-insensitive",
			rule:      PolicyRule{ExecutionLocation: "Downloads"},
			ctx:       PolicyContext{ExecutionLocation: "downloads"},
			wantScore: 1,
			wantOK:    true,
		},
		{
			name:      "multiple dimensions all matching sum the score",
			rule:      PolicyRule{AppStatus: "blocked", ExecutionLocation: "downloads", DeviceGroup: "Lantai 1"},
			ctx:       PolicyContext{AppStatus: "blocked", ExecutionLocation: "downloads", DeviceGroup: "Lantai 1"},
			wantScore: 3,
			wantOK:    true,
		},
		{
			name:      "one mismatching dimension fails the whole rule even if others match",
			rule:      PolicyRule{AppStatus: "blocked", DeviceGroup: "Lantai 2"},
			ctx:       PolicyContext{AppStatus: "blocked", DeviceGroup: "Lantai 1"},
			wantScore: 0,
			wantOK:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			gotScore, gotOK := MatchScore(&tc.rule, tc.ctx)

			// Assert
			if gotOK != tc.wantOK {
				t.Errorf("MatchScore() ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotScore != tc.wantScore {
				t.Errorf("MatchScore() score = %d, want %d", gotScore, tc.wantScore)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	t.Run("no rules defaults to log", func(t *testing.T) {
		// Act
		decision := Evaluate(nil, PolicyContext{AppStatus: "blocked"})

		// Assert
		if decision.Action != PolicyActionLog {
			t.Errorf("Action = %q, want %q", decision.Action, PolicyActionLog)
		}
		if decision.Rule != nil {
			t.Error("Rule should be nil when nothing matched")
		}
	})

	t.Run("most-specific-wins: more matching dimensions beats fewer", func(t *testing.T) {
		// Arrange
		rules := []PolicyRule{
			{ID: 1, AppStatus: "blocked", Action: PolicyActionLog},
			{ID: 2, AppStatus: "blocked", ExecutionLocation: "downloads", Action: PolicyActionKill},
		}
		ctx := PolicyContext{AppStatus: "blocked", ExecutionLocation: "downloads"}

		// Act
		decision := Evaluate(rules, ctx)

		// Assert
		if decision.Action != PolicyActionKill {
			t.Errorf("Action = %q, want %q (the more specific rule)", decision.Action, PolicyActionKill)
		}
		if decision.Rule == nil || decision.Rule.ID != 2 {
			t.Errorf("Rule = %+v, want rule ID 2", decision.Rule)
		}
	})

	t.Run("tie broken by lowest ID (oldest rule first)", func(t *testing.T) {
		// Arrange
		rules := []PolicyRule{
			{ID: 5, AppStatus: "blocked", Action: PolicyActionNotify},
			{ID: 2, AppStatus: "blocked", Action: PolicyActionKill},
		}
		ctx := PolicyContext{AppStatus: "blocked"}

		// Act
		decision := Evaluate(rules, ctx)

		// Assert
		if decision.Rule == nil || decision.Rule.ID != 2 {
			t.Errorf("Rule = %+v, want the lowest-ID tied rule (ID 2)", decision.Rule)
		}
	})

	t.Run("disabled rules must be pre-filtered by the caller — Evaluate itself doesn't check Enabled", func(t *testing.T) {
		// Arrange — this documents the existing contract: GetEnabledPolicyRules
		// filters before calling Evaluate, Evaluate has no Enabled check of its own.
		rules := []PolicyRule{{ID: 1, AppStatus: "blocked", Action: PolicyActionKill, Enabled: false}}
		ctx := PolicyContext{AppStatus: "blocked"}

		// Act
		decision := Evaluate(rules, ctx)

		// Assert
		if decision.Action != PolicyActionKill {
			t.Errorf("Action = %q, want %q (Evaluate does not filter by Enabled)", decision.Action, PolicyActionKill)
		}
	})
}
