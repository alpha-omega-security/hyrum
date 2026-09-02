package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePrivateWorkRootCreates0700Directory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "hyrum")
	if err := ensurePrivateWorkRoot(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %#o, want 0700", got)
	}
}

func TestDefaultWorkPathsUseDistinctUserCacheSubdirectories(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := defaultWorkPath("work"), filepath.Join(cache, "hyrum", "work"); got != want {
		t.Fatalf("gen work path = %q, want %q", got, want)
	}
	if got, want := defaultWorkPath("corpus"), filepath.Join(cache, "hyrum", "corpus"); got != want {
		t.Fatalf("corpus work path = %q, want %q", got, want)
	}
}

func TestEnsurePrivateWorkRootRejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	link := filepath.Join(parent, "work")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateWorkRoot(link); err == nil {
		t.Fatal("default work root accepted a symlink")
	}
}
