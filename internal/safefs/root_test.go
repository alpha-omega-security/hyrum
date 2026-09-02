package safefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "output.json")); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.WriteFile("output.json", []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Fatalf("external file changed to %q", got)
	}
	if info, err := os.Lstat(filepath.Join(dir, "output.json")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("output was not replaced by a regular file: %v, %v", info, err)
	}
}

func TestReadRegularRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(victim, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "output.json")); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.ReadRegular("output.json"); err == nil {
		t.Fatal("ReadRegular followed a symlink")
	}
}

func TestRootRejectsEscape(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.WriteFile("../outside", []byte("bad"), 0o644); err == nil {
		t.Fatal("WriteFile accepted an escaping path")
	}
}
