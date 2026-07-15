package main

import (
	"log/slog"
	"time"
)

const (
	batchFlushInterval = 10 * time.Second
	batchBufferSize    = 1000
)

type metricEntry struct {
	agentID string
	cpu     float64
	ram     float64
	ts      time.Time
}

// MetricsBatcher buffers incoming metric writes and flushes them to SQLite
// in a single transaction every 10 seconds, reducing write IOPS significantly
// under high agent load.
type MetricsBatcher struct {
	ch   chan metricEntry
	db   *DB
	done chan struct{}
}

func NewMetricsBatcher(db *DB) *MetricsBatcher {
	b := &MetricsBatcher{
		ch:   make(chan metricEntry, batchBufferSize),
		db:   db,
		done: make(chan struct{}),
	}
	go b.run()
	return b
}

func (b *MetricsBatcher) Add(agentID string, cpu, ram float64) {
	select {
	case b.ch <- metricEntry{agentID: agentID, cpu: cpu, ram: ram, ts: nowWIB()}:
	default:
		slog.Warn("metrics batcher: buffer full, dropping metric", "agent_id", agentID)
	}
}

func (b *MetricsBatcher) Stop() {
	close(b.done)
}

func (b *MetricsBatcher) run() {
	ticker := time.NewTicker(batchFlushInterval)
	defer ticker.Stop()
	buf := make([]metricEntry, 0, 64)
	for {
		select {
		case e := <-b.ch:
			buf = append(buf, e)
			if len(buf) >= batchBufferSize {
				b.flush(buf)
				buf = buf[:0]
			}
		case <-ticker.C:
			if len(buf) > 0 {
				b.flush(buf)
				buf = buf[:0]
			}
		case <-b.done:
			// Drain remaining entries before exit.
			for len(b.ch) > 0 {
				buf = append(buf, <-b.ch)
			}
			if len(buf) > 0 {
				b.flush(buf)
			}
			return
		}
	}
}

func (b *MetricsBatcher) flush(entries []metricEntry) {
	tx, err := b.db.Begin()
	if err != nil {
		slog.Error("metrics batcher: begin tx", "error", err)
		return
	}
	stmt, err := tx.Prepare(
		`INSERT INTO metrics (agent_id, cpu, ram, recorded_at) VALUES (?, ?, ?, ?)`,
	)
	if err != nil {
		tx.Rollback()
		slog.Error("metrics batcher: prepare stmt", "error", err)
		return
	}
	defer stmt.Close()
	for _, e := range entries {
		if _, err := stmt.Exec(e.agentID, e.cpu, e.ram, fmtTime(e.ts)); err != nil {
			slog.Error("metrics batcher: insert row", "agent_id", e.agentID, "error", err)
		}
	}
	if err := tx.Commit(); err != nil {
		tx.Rollback()
		slog.Error("metrics batcher: commit", "error", err)
		return
	}
	slog.Debug("metrics batcher: flushed", "count", len(entries))
}
