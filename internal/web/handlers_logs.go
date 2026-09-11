package web

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/docker/docker/pkg/stdcopy"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/go-chi/chi/v5"
)

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	a, err := s.Store.GetAppByName(r.Context(), name)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	_, err = composer.Parse([]byte(a.ComposeYAML))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	service := r.URL.Query().Get("service")
	if service == "" {
		service = r.URL.Query().Get("services")
	}
	stream := r.URL.Query().Get("stream") == "1"
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "200"
	}

	cs, err := s.Cli.ListContainersFiltered(r.Context(), true, map[string][]string{
		"label": {composer.LabelApp + "=" + a.Slug},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(cs) == 0 {
		http.Error(w, "no containers", http.StatusNotFound)
		return
	}

	var targets []string
	for _, c := range cs {
		full, err := s.Cli.LookupID(r.Context(), c.ID)
		if err != nil || full == "" {
			continue
		}
		if service != "" {
			// Optional refinement: filter by service label via inspect
			// (cheap: only inspect when service filter is on).
			inspect, err := s.Cli.Raw().ContainerInspect(r.Context(), full)
			if err == nil {
				svc, _ := inspect.Config.Labels[composer.LabelService]
				if svc != service {
					continue
				}
			}
		}
		targets = append(targets, full)
	}
	if len(targets) == 0 {
		http.Error(w, "no matching containers", http.StatusNotFound)
		return
	}

	if stream {
		s.streamLogs(w, r, targets, tail)
		return
	}

	// Non-streaming path: dump last N lines of each container.
	for _, id := range targets {
		rc, err := s.Cli.ContainerLogs(r.Context(), id, false, tail)
		if err != nil {
			fmt.Fprintf(w, "[%s] error: %v\n", id, err)
			continue
		}
		s.demux(w, rc)
		rc.Close()
	}
}

func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request, targets []string, tail string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Multiple containers stream concurrently into one response; the
	// writer must be serialized or frames interleave mid-line.
	out := &lockedWriter{w: w, f: flusher}

	for _, id := range targets {
		id := id
		go func() {
			rc, err := s.Cli.ContainerLogs(ctx, id, true, tail)
			if err != nil {
				out.printf("data: [%s] error: %v\n\n", id, err)
				return
			}
			defer rc.Close()
			s.demuxSSE(out, rc)
		}()
	}

	<-r.Context().Done()
}

// lockedWriter serializes SSE writes from the per-container goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
	f  http.Flusher
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.w.Write(p)
	l.f.Flush()
	return n, err
}

func (l *lockedWriter) printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, format, args...)
	l.f.Flush()
}

func (s *Server) demux(w io.Writer, src io.Reader) {
	if _, err := stdcopy.StdCopy(w, w, src); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(w, "[decode error]", err)
	}
}

func (s *Server) demuxSSE(w io.Writer, src io.Reader) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(pw, pw, src)
		pw.Close()
	}()
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		// Escape SSE-forbidden newlines.
		line = bytes.ReplaceAll(line, []byte{'\n'}, nil)
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
}
