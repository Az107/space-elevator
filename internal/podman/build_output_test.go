package podman

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadBuildOutputEOFIsSuccess(t *testing.T) {
	if err := readBuildOutput(strings.NewReader(`{"stream":"Step 1/2\n"}`+"\n"), nil); err != nil {
		t.Fatalf("clean stream should succeed: %v", err)
	}
}

// TestReadBuildOutputCanceledIsFailure is the regression guard: a build whose
// context is cancelled must NOT report success. Returning nil here would let
// the deploy pipeline proceed to Activate using the previous image that still
// carries the tag, deploying stale code and reporting it as green.
func TestReadBuildOutputCanceledIsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &cancelReader{ctx: ctx, r: strings.NewReader(`{"stream":"Step 1/2\n"}` + "\n")}
	err := readBuildOutput(r, nil)
	if err == nil {
		t.Fatal("cancelled build reported success")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error does not unwrap to context.Canceled: %v", err)
	}
}

// TestReadBuildOutputTruncatedStreamIsFailure covers a stream that ends
// mid-message (daemon restart, socket reset): the decoder sees an unexpected
// EOF rather than a clean one and must not be mistaken for success.
func TestReadBuildOutputTruncatedStreamIsFailure(t *testing.T) {
	err := readBuildOutput(strings.NewReader(`{"stream":"Step 1/2`), nil)
	if err == nil {
		t.Fatal("truncated build stream reported success")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("truncated stream should not unwrap to a clean io.EOF: %v", err)
	}
}

func TestReadBuildOutputSurfacesErrorMessage(t *testing.T) {
	stream := `{"errorDetail":{"message":"unknown instruction: FOO"},"error":"build failed"}` + "\n"
	err := readBuildOutput(strings.NewReader(stream), nil)
	if err == nil {
		t.Fatal("build error message was not surfaced")
	}
	if !strings.Contains(err.Error(), "unknown instruction: FOO") {
		t.Fatalf("error missing detail: %v", err)
	}
}

type cancelReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *cancelReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
