package service

import (
	"strings"
	"testing"
)

func TestRenderContainsExecStartAndEnvFile(t *testing.T) {
	body, err := Render(Options{
		BinPath: "/home/me/.local/bin/space-elevator",
		Addr:    "0.0.0.0:8080",
		EnvFile: "/home/me/.config/space-elevator/env",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"ExecStart=/home/me/.local/bin/space-elevator serve --addr 0.0.0.0:8080",
		"EnvironmentFile=-/home/me/.config/space-elevator/env",
		"WantedBy=default.target",
		"Restart=always",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestRenderOmitsEnvFileWhenUnset(t *testing.T) {
	body, err := Render(Options{BinPath: "/bin/se", Addr: "127.0.0.1:9000"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(body, "EnvironmentFile") {
		t.Errorf("did not expect EnvironmentFile:\n%s", body)
	}
}

func TestRenderRequiresBinary(t *testing.T) {
	if _, err := Render(Options{Addr: "127.0.0.1:8080"}); err == nil {
		t.Error("expected error without a binary path")
	}
}

func TestRenderDefaultsAddr(t *testing.T) {
	body, err := Render(Options{BinPath: "/bin/se"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(body, "--addr 127.0.0.1:8080") {
		t.Errorf("expected default addr:\n%s", body)
	}
}
