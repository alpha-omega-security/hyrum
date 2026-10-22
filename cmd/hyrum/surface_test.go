package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
)

func TestSurfaceSummaryReturnsContextCancellation(t *testing.T) {
	target := &hyrum.Target{
		Path: t.TempDir(),
		Deps: []hyrum.Dep{{
			Name:      "flask",
			PURL:      "pkg:pypi/flask",
			Ecosystem: hyrum.EcoPyPI,
			Direct:    true,
		}},
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := surfaceSummaryWithOptions(ctx, target, true, false, usage.IndexOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("surfaceSummary error = %v, want context canceled", err)
	}
}

func TestSurfaceRejectsMissingExplicitConfig(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	err := cmdSurface(t.Context(), []string{"--config", missing, t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "read config") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("error = %v", err)
	}
}

func TestSurfaceRubyImportsAndConstants(t *testing.T) {
	target := t.TempDir()
	files := map[string]string{
		"Gemfile": "source 'https://rubygems.org'\ngem 'octokit'\n",
		"app.rb": "require 'octokit/client'\n" +
			"client = Octokit::Client.new(token: token)\n" +
			"Octokit.configure { |c| c.api = api }\n" +
			"MyOctokit::Client.new\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(target, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	output, err := captureStdout(t, func() error {
		return cmdSurface(t.Context(), []string{"--dep", "octokit", "--json", target})
	})
	if err != nil {
		t.Fatal(err)
	}
	var surfaces []*usage.Surface
	if err := json.Unmarshal([]byte(output), &surfaces); err != nil {
		t.Fatalf("decode surface output: %v\n%s", err, output)
	}
	if len(surfaces) != 1 || surfaces[0].PURL != "pkg:gem/octokit" {
		t.Fatalf("surfaces = %+v", surfaces)
	}
	sites := map[string]int{}
	for _, symbol := range surfaces[0].Symbols {
		sites[symbol.Name] = len(symbol.Sites)
	}
	for _, name := range []string{"octokit/client", "Octokit.Client", "Octokit.configure"} {
		if sites[name] != 1 {
			t.Errorf("%s sites = %d, want 1: %s", name, sites[name], output)
		}
	}
	if sites["MyOctokit.Client"] != 0 {
		t.Errorf("unrelated constant included: %s", output)
	}
}

func TestSurfaceKeepsImportAndActivationKindsSeparate(t *testing.T) {
	target := t.TempDir()
	files := map[string]string{
		"requirements.txt": "aiosqlite==0.20.0\n",
		"hyrum.yaml":       "deps:\n  aiosqlite:\n    activations:\n      - aiosqlite\n",
		"app.py":           "database_driver = \"aiosqlite\"\n",
		"z_import.py":      "import aiosqlite\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(target, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	output, err := captureStdout(t, func() error {
		return cmdSurface(t.Context(), []string{"--dep", "aiosqlite", "--json", target})
	})
	if err != nil {
		t.Fatal(err)
	}
	var surfaces []*usage.Surface
	if err := json.Unmarshal([]byte(output), &surfaces); err != nil {
		t.Fatalf("decode surface output: %v\n%s", err, output)
	}
	if len(surfaces) != 1 {
		t.Fatalf("surfaces = %d, want 1", len(surfaces))
	}

	var activation, imported *usage.Symbol
	for i := range surfaces[0].Symbols {
		symbol := &surfaces[0].Symbols[i]
		if symbol.Name != "aiosqlite" {
			continue
		}
		if symbol.Kind == "activation" {
			activation = symbol
		} else {
			imported = symbol
		}
	}
	if activation == nil || len(activation.Sites) != 1 || activation.Sites[0].File != "app.py" {
		t.Errorf("activation symbol = %#v", activation)
	}
	if imported == nil || len(imported.Sites) != 1 || imported.Sites[0].File != "z_import.py" {
		t.Errorf("import symbol = %#v", imported)
	}
}

func captureStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = out
	defer func() { os.Stdout = original }()

	runErr := run()
	os.Stdout = original
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body), runErr
}

func TestSurfaceSymbolRequiresOneDependency(t *testing.T) {
	err := cmdSurface(t.Context(), []string{"--symbol", "Session", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "--symbol requires exactly one --dep") {
		t.Fatalf("error = %v", err)
	}
}
