package podman

import (
	"strings"
	"testing"
)

func TestReadBuildOutput(t *testing.T) {
	ok := strings.NewReader(`{"stream":"Step 1/3 : FROM busybox\n"}
{"stream":" ---> abc123\n"}
{"aux":{"ID":"sha256:deadbeef"}}
{"stream":"Successfully built abc123\n"}
`)
	if err := readBuildOutput(ok, nil); err != nil {
		t.Errorf("clean stream: %v", err)
	}

	failed := strings.NewReader(`{"stream":"Step 2/3 : RUN echo <h1>broken</h1>\n"}
{"errorDetail":{"message":"/bin/sh: can't open h1: no such file"},"error":"The command '/bin/sh -c ...' returned a non-zero code: 1"}
`)
	err := readBuildOutput(failed, nil)
	if err == nil {
		t.Fatal("error stream must fail")
	}
	if !strings.Contains(err.Error(), "image build failed") {
		t.Errorf("error = %v, want 'image build failed: ...'", err)
	}
	// The human-readable errorDetail.message is preferred over the
	// generic "returned a non-zero code" error string.
	if !strings.Contains(err.Error(), "can't open h1") {
		t.Errorf("error should carry the detail message, got: %v", err)
	}

	truncated := strings.NewReader(`{"stream":"partial`)
	// A truncated stream (connection died mid-build) must not pass.
	if err := readBuildOutput(truncated, nil); err == nil {
		t.Error("truncated stream must fail")
	}
}

func TestReadBuildOutputLogs(t *testing.T) {
	stream := strings.NewReader(`{"stream":"Step 1/2 : FROM busybox\n"}
{"stream":" ---> abc123\nStep 2/2 : RUN true\n"}
{"aux":{"ID":"sha256:deadbeef"}}
`)
	var lines []string
	if err := readBuildOutput(stream, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("clean stream: %v", err)
	}
	want := []string{"Step 1/2 : FROM busybox", " ---> abc123", "Step 2/2 : RUN true"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines (%q), want %d", len(lines), lines, len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}
