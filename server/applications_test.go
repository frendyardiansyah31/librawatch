package main

import "testing"

// Positive: updating the install-detected row (exe_name=”) propagates
// status/category_id to the process-detected sibling row sharing the same
// (product_name, company) — the split-identity bug behind the Zoom
// auto-kill report (GetPolicyRelevantApps requires exe_name <> ”, so
// without this propagation a block applied to the exe_name=” row would
// never reach the policy engine).
func TestUpdateApplicationStatus_PropagatesToSiblingRow(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	now := "2026-01-01 00:00:00"

	res, err := db.Exec(
		`INSERT INTO applications (exe_name, company, product_name, status, created_at, updated_at)
		 VALUES ('', 'Zoom Video Communications', 'Zoom', 'pending_review', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("insert install-detected row: %v", err)
	}
	installRowID, _ := res.LastInsertId()

	res, err = db.Exec(
		`INSERT INTO applications (exe_name, company, product_name, status, created_at, updated_at)
		 VALUES ('Zoom.exe', 'Zoom Video Communications', 'Zoom', 'pending_review', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("insert process-detected row: %v", err)
	}
	processRowID, _ := res.LastInsertId()

	catRes, err := db.Exec(`INSERT INTO categories (name) VALUES ('Communication')`)
	if err != nil {
		t.Fatalf("insert category: %v", err)
	}
	categoryID, _ := catRes.LastInsertId()

	// Act — mark the install-detected row (the one an admin editing the
	// dashboard's Applications list has a 50/50 chance of hitting) blocked.
	if err := db.UpdateApplicationStatus(installRowID, "blocked", &categoryID); err != nil {
		t.Fatalf("UpdateApplicationStatus: %v", err)
	}

	// Assert — the process-detected sibling (exe_name='Zoom.exe', the one
	// GetPolicyRelevantApps actually reads) must also be blocked.
	var status string
	var gotCategoryID int64
	if err := db.QueryRow(`SELECT status, category_id FROM applications WHERE id = ?`, processRowID).
		Scan(&status, &gotCategoryID); err != nil {
		t.Fatalf("query sibling row: %v", err)
	}
	if status != "blocked" {
		t.Errorf("sibling row status = %q, want %q", status, "blocked")
	}
	if gotCategoryID != categoryID {
		t.Errorf("sibling row category_id = %d, want %d", gotCategoryID, categoryID)
	}
}

// Negative: rows sharing an empty product_name must never be linked to each
// other — otherwise every install-detected-but-unresolved app (exe_name=”
// AND product_name=”) would incorrectly get merged together.
func TestUpdateApplicationStatus_SkipsPropagationWhenProductNameEmpty(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	now := "2026-01-01 00:00:00"

	res, err := db.Exec(
		`INSERT INTO applications (exe_name, company, product_name, status, created_at, updated_at)
		 VALUES ('unrelated1.exe', '', '', 'pending_review', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("insert row 1: %v", err)
	}
	row1ID, _ := res.LastInsertId()

	res, err = db.Exec(
		`INSERT INTO applications (exe_name, company, product_name, status, created_at, updated_at)
		 VALUES ('unrelated2.exe', '', '', 'pending_review', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("insert row 2: %v", err)
	}
	row2ID, _ := res.LastInsertId()

	// Act
	if err := db.UpdateApplicationStatus(row1ID, "blocked", nil); err != nil {
		t.Fatalf("UpdateApplicationStatus: %v", err)
	}

	// Assert — row2 must be untouched.
	var status string
	if err := db.QueryRow(`SELECT status FROM applications WHERE id = ?`, row2ID).Scan(&status); err != nil {
		t.Fatalf("query row2: %v", err)
	}
	if status != "pending_review" {
		t.Errorf("unrelated row2 status = %q, want unchanged %q", status, "pending_review")
	}
}
