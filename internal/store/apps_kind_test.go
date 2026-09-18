package store

import (
	"path/filepath"
	"testing"
)

func TestAppKindFieldsRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a := &App{
		ID:             "k1",
		Name:           "my-fn",
		SourceType:     "git",
		Kind:           KindFunction,
		Runtime:        "python",
		RuntimeVersion: "3.12",
		Entrypoint:     "handler.py:handler",
		ScaleToZero:    true,
		IdleTimeout:    30,
		Env:            map[string]string{},
	}
	if err := st.CreateApp(t.Context(), a); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetAppByName(t.Context(), "my-fn")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindFunction || got.Runtime != "python" || got.RuntimeVersion != "3.12" ||
		got.Entrypoint != "handler.py:handler" || !got.ScaleToZero || got.IdleTimeout != 30 {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	got.Kind = KindCustom
	got.Runtime = ""
	got.ScaleToZero = false
	if err := st.UpdateApp(t.Context(), got); err != nil {
		t.Fatal(err)
	}
	again, err := st.GetAppByName(t.Context(), "my-fn")
	if err != nil {
		t.Fatal(err)
	}
	if again.Kind != KindCustom || again.Runtime != "" || again.ScaleToZero {
		t.Fatalf("update round trip mismatch: %+v", again)
	}
}

func TestAppKindDefaultsToWeb(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.CreateApp(t.Context(), &App{
		ID: "w1", Name: "site", SourceType: "git", Env: map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAppByName(t.Context(), "site")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindWeb {
		t.Errorf("kind = %q, want %q", got.Kind, KindWeb)
	}
}
