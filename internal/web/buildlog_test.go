package web

import (
	"fmt"
	"testing"
)

func TestBuildLogRingAndSince(t *testing.T) {
	var b buildLog
	total := buildLogMax + 50
	for i := 0; i < total; i++ {
		b.Append(fmt.Sprintf("line %d", i))
	}

	lines, seq := b.Since(0)
	if len(lines) != buildLogMax {
		t.Fatalf("kept %d lines, want %d", len(lines), buildLogMax)
	}
	if seq != total {
		t.Errorf("seq = %d, want %d", seq, total)
	}
	// The oldest lines were dropped, the newest kept in order.
	if lines[0].Text != fmt.Sprintf("line %d", total-buildLogMax) {
		t.Errorf("first kept line = %q", lines[0].Text)
	}
	if lines[len(lines)-1].Text != fmt.Sprintf("line %d", total-1) {
		t.Errorf("last kept line = %q", lines[len(lines)-1].Text)
	}

	// Incremental reads only return what the client hasn't seen.
	inc, seq2 := b.Since(seq - 3)
	if len(inc) != 3 {
		t.Fatalf("incremental read returned %d lines, want 3", len(inc))
	}
	if seq2 != seq {
		t.Errorf("seq moved backwards: %d -> %d", seq, seq2)
	}

	// Empty lines are dropped, not recorded.
	before, _ := b.Since(0)
	b.Append("")
	after, seqAfter := b.Since(before[len(before)-1].Seq)
	if len(after) != 0 || seqAfter != seq {
		t.Error("empty Append must not add a line or advance seq")
	}
}

func TestBuildLogStageAndReset(t *testing.T) {
	var b buildLog
	b.Stage("clone")
	b.Append("Cloning https://example.com/repo.git (main)...")
	lines, seq := b.Since(0)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0].Stage != "clone" || lines[0].Text != "" {
		t.Errorf("stage line = %+v, want stage=clone, empty text", lines[0])
	}
	if lines[1].Stage != "" || lines[1].Text == "" {
		t.Errorf("output line = %+v, want stage empty, text set", lines[1])
	}

	// Reset clears the output but the sequence must stay monotonic, so
	// a client polling from the previous deploy's seq never re-reads
	// (or permanently skips) lines of the next deploy.
	b.Reset()
	lines, seqAfter := b.Since(0)
	if len(lines) != 0 {
		t.Errorf("reset left %d lines", len(lines))
	}
	if seqAfter != seq {
		t.Errorf("seq not monotonic across reset: %d -> %d", seq, seqAfter)
	}
}

func TestBuildLogRegistry(t *testing.T) {
	r := newBuildLogRegistry()
	r.Append("a", "one")
	r.Append("a", "two")
	r.Append("b", "other")

	lines, seq := r.Since("a", 0)
	if len(lines) != 2 || seq != 2 {
		t.Errorf("a: lines=%d seq=%d, want 2/2", len(lines), seq)
	}
	lines, _ = r.Since("b", 0)
	if len(lines) != 1 {
		t.Errorf("b: lines=%d, want 1", len(lines))
	}

	r.Reset("a")
	lines, _ = r.Since("a", 0)
	if len(lines) != 0 {
		t.Errorf("a after reset: lines=%d, want 0", len(lines))
	}

	// Removal drops the sink; access lazily recreates an empty one.
	r.Remove("b")
	lines, _ = r.Since("b", 0)
	if len(lines) != 0 {
		t.Errorf("b after remove: lines=%d, want 0", len(lines))
	}
}
