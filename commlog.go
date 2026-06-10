// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"sync"
	"time"
)

// CommLogEntry is one recorded communication event between The Moment and a printer.
// Entries are held in-memory only; they are never persisted to the database.
type CommLogEntry struct {
	ID        int64     `json:"id"`
	Time      time.Time `json:"time"`
	Dir       string    `json:"dir"`    // "TX" | "RX" | "EV"
	EventType string    `json:"type"`   // "poll_status" | "poll_job" | "push_recv" | "mqtt_recv" | "state_change" | "connect" | "error"
	Summary   string    `json:"summary"`
	Detail    string    `json:"detail,omitempty"`
}

const commLogMaxSize = 500

// PrinterCommLog is a fixed-size in-memory ring buffer of communication events
// for a single printer. Safe for concurrent use.
type PrinterCommLog struct {
	mu      sync.RWMutex
	entries []CommLogEntry
	nextID  int64
}

// Append adds a new entry, dropping the oldest when the buffer is full.
func (cl *PrinterCommLog) Append(dir, evType, summary, detail string) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	entry := CommLogEntry{
		ID:        cl.nextID,
		Time:      time.Now(),
		Dir:       dir,
		EventType: evType,
		Summary:   summary,
		Detail:    detail,
	}
	cl.nextID++
	cl.entries = append(cl.entries, entry)
	if len(cl.entries) > commLogMaxSize {
		cl.entries = cl.entries[len(cl.entries)-commLogMaxSize:]
	}
}

// Recent returns a copy of the last n entries (or all if fewer than n exist).
func (cl *PrinterCommLog) Recent(n int) []CommLogEntry {
	cl.mu.RLock()
	defer cl.mu.RUnlock()
	if len(cl.entries) == 0 {
		return nil
	}
	start := 0
	if len(cl.entries) > n {
		start = len(cl.entries) - n
	}
	result := make([]CommLogEntry, len(cl.entries)-start)
	copy(result, cl.entries[start:])
	return result
}

// Since returns a copy of all entries with ID strictly greater than afterID.
func (cl *PrinterCommLog) Since(afterID int64) []CommLogEntry {
	cl.mu.RLock()
	defer cl.mu.RUnlock()
	var result []CommLogEntry
	for _, e := range cl.entries {
		if e.ID > afterID {
			result = append(result, e)
		}
	}
	return result
}
