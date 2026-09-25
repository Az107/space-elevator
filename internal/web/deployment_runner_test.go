package web

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestDeploymentRunnerStopRejectsNewWorkAndWaitsForActive(t *testing.T) {
	r := newDeploymentRunner(1)
	started := make(chan struct{})
	release := make(chan struct{})
	var ran atomic.Int32
	r.submit(func() {
		ran.Add(1)
		close(started)
		<-release
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	stopped := make(chan struct{})
	go func() {
		r.stopRunner()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("stop returned before active job finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not wait for active job")
	}
	r.submit(func() { ran.Add(1) })
	if got := ran.Load(); got != 1 {
		t.Fatalf("job count=%d, want 1", got)
	}
}
