package store

import (
	"testing"
	"time"
)

func TestRecordAndListAudit(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	ev := &AuditEvent{
		ActorType:  "user",
		ActorID:    "u1",
		ActorLabel: "admin",
		Action:     "app.deploy",
		TargetType: "app",
		TargetID:   "a1",
		TargetName: "web",
		Outcome:    "success",
		IP:         "203.0.113.9",
		UserAgent:  "curl/8",
		Detail:     "git source",
	}
	if err := s.RecordAudit(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if ev.ID == 0 {
		t.Error("recorded event should get an id")
	}

	got, err := s.ListAudit(ctx, AuditFilter{Action: "app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1", len(got))
	}
	e := got[0]
	if e.ActorLabel != "admin" || e.TargetName != "web" || e.Outcome != "success" || e.IP != "203.0.113.9" {
		t.Errorf("unexpected event %+v", e)
	}
	if e.At.IsZero() {
		t.Error("event timestamp not set")
	}
}

func TestListAuditFilters(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	for _, e := range []*AuditEvent{
		{Action: "auth.login", Outcome: "failure", ActorType: "anonymous", TargetName: "admin"},
		{Action: "auth.login", Outcome: "success", ActorType: "user", ActorID: "u1", TargetName: "admin"},
		{Action: "app.remove", Outcome: "success", ActorType: "user", ActorID: "u1", TargetName: "web"},
	} {
		if err := s.RecordAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	if got, _ := s.ListAudit(ctx, AuditFilter{Action: "auth.login"}); len(got) != 2 {
		t.Errorf("login events = %d, want 2", len(got))
	}
	if got, _ := s.ListAudit(ctx, AuditFilter{Outcome: "failure"}); len(got) != 1 {
		t.Errorf("failure events = %d, want 1", len(got))
	}
	if got, _ := s.ListAudit(ctx, AuditFilter{ActorType: "user"}); len(got) != 2 {
		t.Errorf("user events = %d, want 2", len(got))
	}
	if got, _ := s.ListAudit(ctx, AuditFilter{Target: "web"}); len(got) != 1 {
		t.Errorf("target events = %d, want 1", len(got))
	}
	if got, _ := s.ListAudit(ctx, AuditFilter{Limit: 2}); len(got) != 2 {
		t.Errorf("limited events = %d, want 2", len(got))
	}
}

func TestPruneAudit(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	old := &AuditEvent{Action: "auth.login", Outcome: "success", At: time.Now().Add(-100 * 24 * time.Hour)}
	fresh := &AuditEvent{Action: "auth.login", Outcome: "success"}
	if err := s.RecordAudit(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAudit(ctx, fresh); err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneAudit(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1", n)
	}
	got, _ := s.ListAudit(ctx, AuditFilter{})
	if len(got) != 1 || got[0].Outcome != "success" {
		t.Errorf("remaining events = %d, want the fresh one", len(got))
	}
}
