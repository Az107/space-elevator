package commands

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestSecretState(t *testing.T) {
	if got := secretState(""); got != "<unset>" {
		t.Errorf("unset secret = %q", got)
	}
	if got := secretState("do-not-print"); got != "<set>" {
		t.Errorf("set secret = %q", got)
	}
}

func TestConfigShowRedactsTokenManagerSecret(t *testing.T) {
	t.Setenv("SPACE_ELEVATOR_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Setenv("SPACE_ELEVATOR_TOKEN_MANAGER_URL", "http://manager.example")
	t.Setenv("SPACE_ELEVATOR_TOKEN_MANAGER_CLIENT_ID", "app_test")
	t.Setenv("SPACE_ELEVATOR_TOKEN_MANAGER_CLIENT_SECRET", "cs_do_not_print")

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := runConfigShow(cmd, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "cs_do_not_print") {
		t.Fatalf("config show leaked client secret:\n%s", got)
	}
	if !strings.Contains(got, "token_manager_client_secret: <set>") ||
		!strings.Contains(got, "SPACE_ELEVATOR_TOKEN_MANAGER_CLIENT_SECRET=<set>") {
		t.Errorf("config show did not report redacted secret state:\n%s", got)
	}
}
