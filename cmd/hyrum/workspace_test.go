package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteWorkspaceJSONReplacesPlantedSymlink(t *testing.T) {
	ws := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(ws, "usage.json")
	if err := os.Symlink(victim, output); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceJSON(ws, "usage.json", map[string]string{"safe": "value"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Fatalf("external file changed to %q", got)
	}
	if info, err := os.Lstat(output); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("workspace output was not replaced: %v, %v", info, err)
	}
}

func TestPrepareWorkspaceUnderRejectsEscapingDirectorySymlink(t *testing.T) {
	work := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(work, "target")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareWorkspaceUnder(work, filepath.Join("target", "npm", "ws")); err == nil {
		t.Fatal("workspace creation followed a directory symlink outside the work root")
	}
}

func TestLinkWorkspaceDirectoryCreatesExternalInputLink(t *testing.T) {
	ws := t.TempDir()
	target := t.TempDir()
	if err := linkWorkspaceDirectory(ws, "target", target); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(filepath.Join(ws, "target"))
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("target link = %q, want %q", got, target)
	}
}
