package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/git-pkgs/brief"
	"github.com/git-pkgs/managers"
)

func TestSplitDepSpec(t *testing.T) {
	cases := []struct{ in, name, ver string }{
		{"ws", "ws", ""},
		{"ws@8.17.1", "ws", "8.17.1"},
		{"@scope/pkg", "@scope/pkg", ""},
		{"@scope/pkg@1.2.3", "@scope/pkg", "1.2.3"},
		{"flask@2.3.3", "flask", "2.3.3"},
	}
	for _, c := range cases {
		n, v := splitDepSpec(c.in)
		if n != c.name || v != c.ver {
			t.Errorf("%q: got (%q,%q) want (%q,%q)", c.in, n, v, c.name, c.ver)
		}
	}
}

func TestCheckOneRejectsUnsafeDependencyPath(t *testing.T) {
	if _, err := checkOne(context.Background(), nil, nil, t.TempDir(), "../../escape"); err == nil {
		t.Fatal("checkOne accepted a dependency name that escapes the tests root")
	}
}

func TestManagerHintMapsGoModulesToGomod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	target := &hyrum.Target{
		Path: dir,
		Report: &brief.Report{PackageManagers: []brief.Detection{{
			Name:     "Go Modules",
			Lockfile: "go.sum",
		}}},
	}

	hint := managerHint(target)
	if hint != "gomod" {
		t.Fatalf("managerHint = %q, want gomod", hint)
	}
	mgr, err := detectManager(dir, hint)
	if err != nil {
		t.Fatal(err)
	}
	if mgr.Name() != "gomod" {
		t.Fatalf("detected manager = %q, want gomod", mgr.Name())
	}
}

func TestDetectManagerForGoUsesGomod(t *testing.T) {
	mgr, err := detectManagerFor(t.TempDir(), hyrum.EcoGo)
	if err != nil {
		t.Fatal(err)
	}
	if mgr.Name() != "gomod" {
		t.Fatalf("detected manager = %q, want gomod", mgr.Name())
	}
}

type addErrorManager struct {
	managers.Manager
}

func (addErrorManager) Name() string      { return "test" }
func (addErrorManager) Ecosystem() string { return hyrum.EcoNPM }
func (addErrorManager) Add(context.Context, string, managers.AddOptions) (*managers.Result, error) {
	return nil, errors.New("invalid package")
}

func TestCheckOneHandlesAddErrorWithoutResult(t *testing.T) {
	testsRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(testsRoot, "example"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := &hyrum.Target{Path: t.TempDir()}

	ok, err := checkOne(t.Context(), target, addErrorManager{}, testsRoot, "example@1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("checkOne reported a failed install as successful")
	}
}
