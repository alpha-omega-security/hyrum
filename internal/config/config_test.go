package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadExplicitConfig(t *testing.T) {
	target := t.TempDir()
	path := filepath.Join(t.TempDir(), "custom.yaml")
	writeTestConfig(t, path, `
backend: codex
out: generated/hyrum
work: workspaces
models:
  hyrum-generate: high
deps:
  ws:
    baseline: 8.17.1
    skip: false
`)

	cfg, source, err := Load(target, path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if source != path {
		t.Fatalf("source = %q, want %q", source, path)
	}
	if cfg.Backend == nil || *cfg.Backend != "codex" {
		t.Fatalf("backend = %v", cfg.Backend)
	}
	if got := cfg.Models["hyrum-generate"]; got != "high" {
		t.Fatalf("generate model = %q", got)
	}
	dep := cfg.Deps["ws"]
	if dep.Baseline == nil || *dep.Baseline != "8.17.1" {
		t.Fatalf("baseline = %v", dep.Baseline)
	}
	if dep.Skip == nil || *dep.Skip {
		t.Fatalf("skip = %v", dep.Skip)
	}
}

func TestLoadDiscoversTargetRootConfigWithoutWalkingParents(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "repo")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestConfig(t, filepath.Join(parent, Filename), "backend: codex\n")

	cfg, source, err := Load(target, "")
	if err != nil {
		t.Fatalf("missing optional config: %v", err)
	}
	if source != "" || cfg.Backend != nil {
		t.Fatalf("walked above target root: source=%q backend=%v", source, cfg.Backend)
	}

	wantSource := filepath.Join(target, Filename)
	writeTestConfig(t, wantSource, "backend: copilot\n")
	cfg, source, err = Load(target, "")
	if err != nil {
		t.Fatalf("discovered config: %v", err)
	}
	if source != wantSource || cfg.Backend == nil || *cfg.Backend != "copilot" {
		t.Fatalf("source=%q backend=%v", source, cfg.Backend)
	}
}

func TestLoadMissingExplicitConfigFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	_, _, err := Load(t.TempDir(), missing)
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("error = %v, want explicit path", err)
	}
}

func TestLoadUnreadableExplicitConfigFails(t *testing.T) {
	path := t.TempDir() // Reading a directory as a config fails on every platform.
	_, _, err := Load(t.TempDir(), path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("error = %v, want explicit path", err)
	}
}

func TestParseRejectsMalformedUnknownAndIncorrectTypes(t *testing.T) {
	tests := map[string]string{
		"malformed":       "backend: [\n",
		"unknown top key": "backed: codex\n",
		"unknown dep key": "deps:\n  ws:\n    basline: 1.0\n",
		"backend type":    "backend: true\n",
		"skip type":       "deps:\n  ws:\n    skip: yes-please\n",
		"null":            "work: null\n",
		"second document": "backend: codex\n---\nbackend: claude\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(body)); err == nil {
				t.Fatal("Parse succeeded, want error")
			}
		})
	}
}

func TestParseTypeErrorExplainsQuotedBaseline(t *testing.T) {
	_, err := Parse([]byte("deps:\n  ws:\n    baseline: 8.17\n"))
	if err == nil {
		t.Fatal("Parse succeeded, want error")
	}
	for _, want := range []string{"must be a quoted string", "!!float", "line 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
}

func TestParseRejectsDuplicateAliasAndMergeKeys(t *testing.T) {
	tests := map[string]string{
		"duplicate": "backend: codex\nbackend: claude\n",
		"alias":     "out: &outside /tmp/out\nwork: *outside\n",
		"merge":     "defaults: &defaults\n  skip: true\ndeps:\n  ws:\n    <<: *defaults\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(body)); err == nil {
				t.Fatal("Parse succeeded, want error")
			}
		})
	}
}

func TestParseRejectsUnsupportedModelSettings(t *testing.T) {
	tests := map[string]string{
		"unknown skill": "models:\n  hyrum-magic: high\n",
		"unknown tier":  "models:\n  hyrum-generate: enormous\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(body)); err == nil {
				t.Fatal("Parse succeeded, want error")
			}
		})
	}
}

func TestConfiguredPathResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(t.TempDir(), "config", Filename)

	tests := []struct {
		name, value, want string
	}{
		{"tilde", "~/.cache/hyrum", filepath.Join(home, ".cache", "hyrum")},
		{"relative", "work/hyrum", filepath.Join(filepath.Dir(configPath), "work", "hyrum")},
		{"absolute", filepath.Join(t.TempDir(), "work"), ""},
	}
	tests[2].want = tests[2].value

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolvePath(configPath, test.value)
			if err != nil {
				t.Fatalf("ResolvePath: %v", err)
			}
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func writeTestConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
