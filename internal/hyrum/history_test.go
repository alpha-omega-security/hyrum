package hyrum

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-pkgs/brief"
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
	if err := streamLog(context.Background(), dir, nil, logMetadata, func(c Commit) {
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
	if err := streamLog(t.Context(), dir, []string{"package.json"}, logNames, func(c Commit) {
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
	tgt := &Target{Path: dir, Report: &brief.Report{PackageManagers: []brief.Detection{{
		ConfigFiles: []string{"package.json"},
	}}}}
	idx, err := BuildHistoryIndex(context.Background(), tgt, []Dep{{Name: "ws"}, {Name: "lodash"}})
	if err != nil {
		t.Fatal(err)
	}
	ws := idx.For("ws")
	if len(ws) != 3 {
		t.Errorf("ws matches = %d, want 3 (add + bump + case-insensitive fix): %+v", len(ws), ws)
	}
	if len(idx.For("lodash")) != 0 {
		t.Errorf("lodash should have 0 matches")
	}
}

func TestBuildHistoryIndexMatchesChangedManifestLinesOnly(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	writePackage := func(ws, lodash string) {
		t.Helper()
		contents := "{\n  \"dependencies\": {\n    \"ws\": \"" + ws + "\",\n    \"lodash\": \"" + lodash + "\"\n  }\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q")
	writePackage("7.4.2", "4.17.20")
	run("add", "package.json")
	run("commit", "-q", "-m", "add dependencies")
	writePackage("7.4.2", "4.17.21")
	run("commit", "-q", "-am", "update utility package")
	writePackage("8.0.0", "4.17.21")
	run("commit", "-q", "-am", "update ws package")

	tgt := &Target{Path: dir, Report: &brief.Report{PackageManagers: []brief.Detection{{
		ConfigFiles: []string{"package.json"},
	}}}}
	idx, err := BuildHistoryIndex(t.Context(), tgt, []Dep{{Name: "ws"}})
	if err != nil {
		t.Fatal(err)
	}
	matches := idx.For("ws")
	if len(matches) != 2 {
		t.Fatalf("ws matches = %d, want initial and ws update commits: %+v", len(matches), matches)
	}
	if matches[0].Subject != "update ws package" {
		t.Errorf("first match subject = %q", matches[0].Subject)
	}
	if got := strings.Join(matches[0].Changes, "\n"); !strings.Contains(got, `-    "ws": "7.4.2",`) || !strings.Contains(got, `+    "ws": "8.0.0",`) {
		t.Errorf("ws update changes = %q", got)
	}
	for _, match := range matches {
		if match.Subject == "update utility package" {
			t.Errorf("unchanged manifest context selected unrelated commit: %+v", match)
		}
	}

	out := filepath.Join(t.TempDir(), "git-log.txt")
	if err := idx.WriteGitLog("ws", out); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `-    "ws": "7.4.2",`) || !strings.Contains(string(contents), `+    "ws": "8.0.0",`) {
		t.Errorf("git-log.txt lacks matching manifest evidence:\n%s", contents)
	}
	if strings.Contains(string(contents), "lodash") {
		t.Errorf("git-log.txt contains a non-matching manifest line:\n%s", contents)
	}
}

func TestTouchedManifestPathsUsesExactDiffHeaders(t *testing.T) {
	patch := "diff --git a/packages/app/package.json b/packages/app/package.json\n"
	paths := []string{"package.json", "packages/app/package.json"}
	got := touchedManifestPaths(patch, paths)
	if len(got) != 1 || got[0] != "packages/app/package.json" {
		t.Fatalf("touchedManifestPaths = %q", got)
	}
}

func TestChangedPatchLinesExcludesDiffHeadersAndContext(t *testing.T) {
	patch := "diff --git a/package.json b/package.json\n--- a/package.json\n+++ b/package.json\n@@ -1,3 +1,3 @@\n {\n-  \"ws\": \"7.4.2\"\n+  \"ws\": \"8.0.0\"\n }\n"
	got := changedPatchLines(patch)
	want := []string{`-  "ws": "7.4.2"`, `+  "ws": "8.0.0"`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("changedPatchLines = %q, want %q", got, want)
	}
}

func TestStreamLogPropagatesError(t *testing.T) {
	err := streamLog(context.Background(), "/nonexistent-repo-path", nil, logMetadata, func(Commit) {})
	if err == nil {
		t.Fatal("want error for non-repo path")
	}
}
