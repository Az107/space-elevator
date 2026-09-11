package web

import (
	"fmt"
	"sync"
)

// buildLogMax caps the in-memory build output per app. Enough to
// review a failed image build; bounded so a noisy build can't grow
// the server's memory without limit.
const buildLogMax = 500

// buildLogLine is one entry in a deploy's build log. Stage is empty
// for ordinary output lines and names a pipeline phase (clone,
// compose, deploy, route) for stage-transition events.
type buildLogLine struct {
	Seq   int    `json:"seq"`
	Text  string `json:"text,omitempty"`
	Stage string `json:"stage,omitempty"`
}

// buildLog is a bounded in-memory sink for one app's deploy progress
// and image-build output. Lines carry a monotonically increasing
// sequence number so clients can poll incrementally (?after=seq).
// Transient by design: it describes a single build and is lost on
// server restart.
type buildLog struct {
	mu    sync.Mutex
	lines []buildLogLine
	seq   int
}

func (b *buildLog) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = nil
}

// Append records one output line, keeping the buffer bounded.
func (b *buildLog) Append(text string) {
	if text == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	b.lines = append(b.lines, buildLogLine{Seq: b.seq, Text: text})
	if len(b.lines) > buildLogMax {
		b.lines = b.lines[len(b.lines)-buildLogMax:]
	}
}

func (b *buildLog) appendf(format string, args ...any) {
	b.Append(fmt.Sprintf(format, args...))
}

// Stage records a pipeline-phase transition for UI progress indicators.
func (b *buildLog) Stage(stage string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	b.lines = append(b.lines, buildLogLine{Seq: b.seq, Stage: stage})
}

// Since returns the stored lines with Seq > after plus the latest
// sequence number (so the client can continue from there).
func (b *buildLog) Since(after int) ([]buildLogLine, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []buildLogLine
	for _, l := range b.lines {
		if l.Seq > after {
			out = append(out, l)
		}
	}
	return out, b.seq
}

// buildLogRegistry holds one sink per app name, created on demand.
type buildLogRegistry struct {
	mu   sync.Mutex
	logs map[string]*buildLog
}

func newBuildLogRegistry() *buildLogRegistry {
	return &buildLogRegistry{logs: map[string]*buildLog{}}
}

func (r *buildLogRegistry) get(name string) *buildLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.logs[name]
	if !ok {
		b = &buildLog{}
		r.logs[name] = b
	}
	return b
}

func (r *buildLogRegistry) Reset(name string) { r.get(name).Reset() }

func (r *buildLogRegistry) Append(name, text string) { r.get(name).Append(text) }

func (r *buildLogRegistry) appendf(name, format string, args ...any) {
	r.get(name).appendf(format, args...)
}

func (r *buildLogRegistry) Stage(name, stage string) { r.get(name).Stage(stage) }

// Since returns an app's lines after the given seq plus the latest seq.
func (r *buildLogRegistry) Since(name string, after int) ([]buildLogLine, int) {
	return r.get(name).Since(after)
}

// Remove drops the sink for a deleted app so the map can't grow with
// every app ever created.
func (r *buildLogRegistry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.logs, name)
}
