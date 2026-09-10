package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `
identity:
  name: "Pedro Tessaro"
  role: "Backend Engineer"
  location: "Brazil"
  github_user: "PedroTessaro"
stack:
  - "Go"
  - "Java"
terminal:
  host: "example.fly.dev"
  user: "tessaro"
  machine: "portfolio"
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeConfig(t, valid))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Identity.Name != "Pedro Tessaro" {
		t.Errorf("name = %q", cfg.Identity.Name)
	}
	if len(cfg.Stack) != 2 {
		t.Errorf("stack has %d entries, want 2", len(cfg.Stack))
	}
}

// Failing at boot beats serving an SVG with empty fields.
func TestLoadRejectsIncompleteConfig(t *testing.T) {
	for name, body := range map[string]string{
		"no name":     strings.Replace(valid, `name: "Pedro Tessaro"`, `name: ""`, 1),
		"no host":     strings.Replace(valid, `host: "example.fly.dev"`, `host: ""`, 1),
		"empty stack": strings.Split(valid, "stack:")[0] + "\nterminal:\n  host: \"h\"\n  user: \"u\"\n  machine: \"m\"\n",
		"no identity": "stack:\n  - Go\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil {
				t.Error("expected a validation error, got nil")
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected an error for a missing file")
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	if _, err := Load(writeConfig(t, "identity: [this: is: not: yaml")); err == nil {
		t.Error("expected a parse error")
	}
}
