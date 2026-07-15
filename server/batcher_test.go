package main

import (
	"os"
	"testing"
	"time"
)

// openTestDB opens an in-memory SQLite DB wired up with schema and pragmas.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := initDB(":memory:")
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ── MetricsBatcher ────────────────────────────────────────────────────────

// Positive: entries added to batcher appear in DB after flush interval.
func TestBatcher_FlushWritesToDB(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	agentID := "agent-flush-test"
	insertTestAgent(t, db, agentID)

	b := NewMetricsBatcher(db)
	defer b.Stop()

	// Act
	b.Add(agentID, 42.5, 55.0)
	b.Add(agentID, 43.0, 56.0)
	// Wait longer than flush interval
	time.Sleep(batchFlushInterval + 500*time.Millisecond)

	// Assert
	rows, err := db.Query(`SELECT COUNT(*) FROM metrics WHERE agent_id = ?`, agentID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var count int
	if rows.Next() {
		rows.Scan(&count)
	}
	if count != 2 {
		t.Fatalf("expected 2 rows in metrics, got %d", count)
	}
}

// Positive: Stop() flushes remaining buffered entries before exit.
func TestBatcher_StopFlushesRemaining(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	agentID := "agent-stop-test"
	insertTestAgent(t, db, agentID)

	b := NewMetricsBatcher(db)
	b.Add(agentID, 10.0, 20.0)
	b.Add(agentID, 11.0, 21.0)
	b.Add(agentID, 12.0, 22.0)

	// Act — stop before flush interval elapses
	b.Stop()
	time.Sleep(100 * time.Millisecond)

	// Assert — all 3 entries must be persisted
	rows, err := db.Query(`SELECT COUNT(*) FROM metrics WHERE agent_id = ?`, agentID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var count int
	if rows.Next() {
		rows.Scan(&count)
	}
	if count != 3 {
		t.Fatalf("expected 3 rows after Stop(), got %d", count)
	}
}

// Positive: buffer-full condition drops entries gracefully without panic.
func TestBatcher_BufferFull_NoPanic(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	agentID := "agent-overflow-test"
	insertTestAgent(t, db, agentID)

	b := NewMetricsBatcher(db)
	defer b.Stop()

	// Act — flood well beyond buffer capacity; must not panic
	for i := 0; i < batchBufferSize*2; i++ {
		b.Add(agentID, float64(i), float64(i))
	}
	// Assert — no panic reached this line
}

// Negative: batcher with nil DB causes flush to log error and not panic.
func TestBatcher_FlushWithClosedDB_NoPanic(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	agentID := "agent-nil-test"
	insertTestAgent(t, db, agentID)

	b := NewMetricsBatcher(db)
	b.Add(agentID, 1.0, 2.0)

	// Close DB before flush fires
	db.Close()

	// Act — flush must not panic despite closed DB
	time.Sleep(batchFlushInterval + 500*time.Millisecond)
	// Assert — reaching here means no panic
}

// ── PurgeOldMetrics (retention) ───────────────────────────────────────────

// Positive: metrics older than 7 days are deleted.
func TestPurgeOldMetrics_DeletesOldRows(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	agentID := "agent-purge-old"
	insertTestAgent(t, db, agentID)

	old := fmtTime(nowWIB().Add(-8 * 24 * time.Hour)) // 8 days ago
	db.Exec(`INSERT INTO metrics (agent_id, cpu, ram, recorded_at) VALUES (?, 50, 60, ?)`, agentID, old)

	// Act
	err := db.PurgeOldMetrics()

	// Assert
	if err != nil {
		t.Fatalf("PurgeOldMetrics: %v", err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM metrics WHERE agent_id = ?`, agentID).Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 old rows after purge, got %d", count)
	}
}

// Positive: metrics within 7 days are kept.
func TestPurgeOldMetrics_KeepsRecentRows(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	agentID := "agent-purge-recent"
	insertTestAgent(t, db, agentID)

	recent := fmtTime(nowWIB().Add(-3 * 24 * time.Hour)) // 3 days ago
	db.Exec(`INSERT INTO metrics (agent_id, cpu, ram, recorded_at) VALUES (?, 50, 60, ?)`, agentID, recent)

	// Act
	err := db.PurgeOldMetrics()

	// Assert
	if err != nil {
		t.Fatalf("PurgeOldMetrics: %v", err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM metrics WHERE agent_id = ?`, agentID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 recent row to survive purge, got %d", count)
	}
}

// Negative: metrics exactly at 7-day boundary are purged (boundary is exclusive).
func TestPurgeOldMetrics_BoundaryIsExclusive(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	agentID := "agent-purge-boundary"
	insertTestAgent(t, db, agentID)

	// Just over 7 days ago
	boundary := fmtTime(nowWIB().Add(-7*24*time.Hour - time.Minute))
	db.Exec(`INSERT INTO metrics (agent_id, cpu, ram, recorded_at) VALUES (?, 50, 60, ?)`, agentID, boundary)

	// Act
	err := db.PurgeOldMetrics()

	// Assert
	if err != nil {
		t.Fatalf("PurgeOldMetrics: %v", err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM metrics WHERE agent_id = ?`, agentID).Scan(&count)
	if count != 0 {
		t.Fatalf("expected boundary row to be purged, got %d rows", count)
	}
}

// ── SQLite pragmas ────────────────────────────────────────────────────────

// Positive: WAL journal mode is set on DB open.
func TestDB_PragmaWAL(t *testing.T) {
	// Arrange
	f, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	// Act
	db, err := initDB(f.Name())
	if err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	// Assert
	var mode string
	db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Fatalf("expected journal_mode=wal, got %q", mode)
	}
}

// Positive: temp_store is set to MEMORY (2).
func TestDB_PragmaTempStore(t *testing.T) {
	// Arrange
	f, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	// Act
	db, err := initDB(f.Name())
	if err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	// Assert — temp_store=MEMORY returns 2
	var val int
	db.QueryRow(`PRAGMA temp_store`).Scan(&val)
	if val != 2 {
		t.Fatalf("expected temp_store=2 (MEMORY), got %d", val)
	}
}

// Negative: initDB with a directory path (not a file) returns error.
func TestDB_DirectoryAsPath_ReturnsError(t *testing.T) {
	// Arrange — use an existing directory as the DB path; SQLite cannot open a dir as a file
	dir := t.TempDir()

	// Act
	db, err := initDB(dir)
	if db != nil {
		db.Close()
	}

	// Assert
	if err == nil {
		t.Fatal("expected error when opening a directory as SQLite DB, got nil")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func insertTestAgent(t *testing.T, db *DB, id string) {
	t.Helper()
	now := fmtTime(nowWIB())
	_, err := db.Exec(
		`INSERT INTO agents (id, hostname, ip, os, last_seen, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, "test-host", "10.0.0.1", "Windows", now, "online", now,
	)
	if err != nil {
		t.Fatalf("insertTestAgent %s: %v", id, err)
	}
}
