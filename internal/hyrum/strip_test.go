package hyrum

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStripAgentDirectives(t *testing.T) {
	root := t.TempDir()
	mk := func(p string, dir bool) {
		full := filepath.Join(root, p)
		if dir {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("CLAUDE.md", false)
	mk("src/AGENTS.md", false)
	mk("src/keep.go", false)
	mk(".claude/settings.json", false)
	mk(".cursor/rules", false)
	mk(".git/refs/heads/claude.md", false) // must survive
	mk("README.md", false)

	n, err := StripAgentDirectives(root)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("removed %d, want 4", n)
	}
	for _, p := range []string{"CLAUDE.md", "src/AGENTS.md", ".claude", ".cursor"} {
		if _, err := os.Stat(filepath.Join(root, p)); !os.IsNotExist(err) {
			t.Errorf("%s not removed", p)
		}
	}
	for _, p := range []string{"src/keep.go", "README.md", ".git/refs/heads/claude.md"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s should survive: %v", p, err)
		}
	}
	// Idempotent.
	n2, err := StripAgentDirectives(root)
	if err != nil || n2 != 0 {
		t.Errorf("second pass: n=%d err=%v", n2, err)
	}
}

func TestStripAgentDirectivesMissingRoot(t *testing.T) {
	n, err := StripAgentDirectives(filepath.Join(t.TempDir(), "nope"))
	if err != nil || n != 0 {
		t.Errorf("missing root: n=%d err=%v", n, err)
	}
}
