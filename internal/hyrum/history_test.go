package hyrum

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{"ws":"7.4.2"}}`), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "add ws dep")
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{"ws":"8.0.0"}}`), 0o644)
	run("commit", "-q", "-am", "bump\n\nunrelated body text\n\nsecond paragraph")
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "fix WS handling\n\nbody with --- in it\nand more")
	return dir
}

func TestStreamLogParsesRecords(t *testing.T) {
	dir := gitInit(t)
	var got []Commit
	if err := streamLog(context.Background(), dir, nil, false, func(c Commit) {
		got = append(got, c)
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("commits = %d, want 3: %+v", len(got), got)
	}
	// Newest first; body containing --- must not split the record.
	if got[0].Subject != "fix WS handling" || !strings.Contains(got[0].Body, "---") {
		t.Errorf("record 0 mis-parsed: %+v", got[0])
	}
	if len(got[0].SHA) != 40 {
		t.Errorf("SHA = %q", got[0].SHA)
	}
}

func TestStreamLogAssociatesNamesWithTheirCommit(t *testing.T) {
	dir := gitInit(t)
	var got []Commit
	if err := streamLog(t.Context(), dir, []string{"package.json"}, true, func(c Commit) {
		got = append(got, c)
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("commits = %d, want 2: %+v", len(got), got)
	}
	for _, commit := range got {
		if len(commit.SHA) != 40 {
			t.Errorf("SHA = %q", commit.SHA)
		}
		if len(commit.Files) != 1 || commit.Files[0] != "package.json" {
			t.Errorf("%q files = %q", commit.Subject, commit.Files)
		}
	}
	if got[0].Subject != "bump" || got[0].Body != "unrelated body text\n\nsecond paragraph" {
		t.Errorf("bump commit = %+v", got[0])
	}
}

func TestBuildHistoryIndexPartitions(t *testing.T) {
	dir := gitInit(t)
	tgt := &Target{Path: dir, Report: nil}
	// manifestPaths reads Report.PackageManagers; nil Report means no
	// manifest scan, which is fine for this test.
	idx, err := BuildHistoryIndex(context.Background(), tgt, []Dep{{Name: "ws"}, {Name: "lodash"}})
	if err != nil {
		t.Fatal(err)
	}
	ws := idx.For("ws")
	if len(ws) != 2 {
		t.Errorf("ws matches = %d, want 2 (add + case-insensitive fix): %+v", len(ws), ws)
	}
	if len(idx.For("lodash")) != 0 {
		t.Errorf("lodash should have 0 matches")
	}
}

func TestStreamLogPropagatesError(t *testing.T) {
	err := streamLog(context.Background(), "/nonexistent-repo-path", nil, false, func(Commit) {})
	if err == nil {
		t.Fatal("want error for non-repo path")
	}
}
