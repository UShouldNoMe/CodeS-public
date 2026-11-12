package main

import (
	"encoding/json"
	"io"
	"os"
	"sort"
	"time"
)

type event map[string]any

// eventWriter is owned by the monitor goroutine. Heartbeats describe observed
// progress, not a guarantee that the Agent or an external service is healthy.
type eventWriter struct {
	encoder                         *json.Encoder
	runID, phase                    string
	started, phaseStarted, progress time.Time
	step, sequence                  int
	active                          map[string]string
}

func newEventWriter(w io.Writer, runID string) *eventWriter {
	now := time.Now()
	return &eventWriter{
		encoder: json.NewEncoder(w), runID: runID, phase: "starting",
		started: now, phaseStarted: now, progress: now,
		active: make(map[string]string),
	}
}

func (w *eventWriter) write(e event) error {
	now := time.Now()
	if e["event"] != "heartbeat" {
		w.progress = now
	}
	if phase, ok := e["phase"].(string); ok && phase != w.phase {
		w.phase, w.phaseStarted = phase, now
	}
	if step, ok := e["step"].(int); ok {
		w.step = step
	}
	switch e["event"] {
	case "tool_started":
		w.active[e["call_id"].(string)] = e["tool"].(string)
	case "tool_finished":
		delete(w.active, e["call_id"].(string))
	case "closing":
		clear(w.active)
	}
	w.sequence++
	e["version"], e["seq"] = 1, w.sequence
	e["run_id"], e["pid"] = w.runID, os.Getpid()
	e["time"] = now.UTC().Format(time.RFC3339Nano)
	e["elapsed_ms"] = now.Sub(w.started).Milliseconds()
	if _, ok := e["phase"]; !ok {
		e["phase"] = w.phase
	}
	if _, ok := e["step"]; !ok {
		e["step"] = w.step
	}
	if e["event"] == "heartbeat" {
		e["idle_ms"] = now.Sub(w.progress).Milliseconds()
		e["phase_elapsed_ms"] = now.Sub(w.phaseStarted).Milliseconds()
		calls := make([]event, 0, len(w.active))
		for id, tool := range w.active {
			calls = append(calls, event{"call_id": id, "tool": tool})
		}
		sort.Slice(calls, func(i, j int) bool { return calls[i]["call_id"].(string) < calls[j]["call_id"].(string) })
		e["active_tools"] = calls
	}
	return w.encoder.Encode(e)
}
