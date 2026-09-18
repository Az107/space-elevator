package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/albertoruiz/space-elevator/internal/store"
)

type fakeStore struct {
	events []*store.AuditEvent
	err    error
}

func (f *fakeStore) RecordAudit(_ context.Context, e *store.AuditEvent) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func TestRecordPersistsAndEmitsJSON(t *testing.T) {
	fs := &fakeStore{}
	var buf bytes.Buffer
	l := New(fs, &buf)

	ctx := WithActor(context.Background(), Actor{Type: ActorUser, ID: "u1", Label: "admin"})
	ctx = WithRequest(ctx, "198.51.100.7", "curl/8")

	l.Record(ctx, Event{
		Action:     ActionAppRemove,
		TargetType: "app",
		TargetID:   "a1",
		TargetName: "web",
		Outcome:    OutcomeSuccess,
	})

	if len(fs.events) != 1 {
		t.Fatalf("persisted events = %d, want 1", len(fs.events))
	}
	got := fs.events[0]
	if got.ActorType != ActorUser || got.ActorID != "u1" || got.ActorLabel != "admin" {
		t.Errorf("actor not propagated: %+v", got)
	}
	if got.IP != "198.51.100.7" || got.UserAgent != "curl/8" {
		t.Errorf("request metadata not propagated: ip=%q ua=%q", got.IP, got.UserAgent)
	}

	line := buf.String()
	if !strings.Contains(line, `"audit":true`) || !strings.Contains(line, `"action":"app.remove"`) {
		t.Errorf("stderr line missing fields: %s", line)
	}
	var wire map[string]any
	if err := json.Unmarshal([]byte(line), &wire); err != nil {
		t.Fatalf("stderr line is not JSON: %v (%s)", err, line)
	}
	if wire["actor_label"] != "admin" || wire["target_name"] != "web" {
		t.Errorf("unexpected wire event: %v", wire)
	}
}

func TestRecordDefaultsActorAndStillEmitsOnStoreError(t *testing.T) {
	fs := &fakeStore{err: errors.New("db down")}
	var buf bytes.Buffer
	l := New(fs, &buf)

	l.Record(context.Background(), Event{Action: ActionLogin, Outcome: OutcomeFailure})

	line := buf.String()
	if !strings.Contains(line, `"actor_type":"system"`) {
		t.Errorf("actor should default to system: %s", line)
	}
	if !strings.Contains(line, "store_error") {
		t.Errorf("store failure should be surfaced: %s", line)
	}
}

func TestNilLoggerAndEmptyOutcome(t *testing.T) {
	var l *Logger
	l.Record(context.Background(), Event{Action: ActionLogin}) // must not panic

	fs := &fakeStore{}
	var buf bytes.Buffer
	New(fs, &buf).Record(context.Background(), Event{Action: ActionLogin})
	if len(fs.events) != 1 || fs.events[0].Outcome != OutcomeSuccess {
		t.Errorf("empty outcome should default to success: %+v", fs.events)
	}
}

func TestContextHelpers(t *testing.T) {
	if a := ActorFromCtx(context.Background()); a.Type != "" {
		t.Errorf("empty ctx actor = %+v", a)
	}
	ctx := WithActor(context.Background(), Actor{Type: ActorToken, ID: "t1"})
	if a := ActorFromCtx(ctx); a.ID != "t1" {
		t.Errorf("actor = %+v", a)
	}
	ip, ua := RequestFromCtx(WithRequest(context.Background(), "1.2.3.4", "ua"))
	if ip != "1.2.3.4" || ua != "ua" {
		t.Errorf("request = %q %q", ip, ua)
	}
}
