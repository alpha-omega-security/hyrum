package hyrum

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alpha-omega-security/harness"
	"github.com/alpha-omega-security/hyrum/internal/safefs"
)

// shHarness embeds ClaudeHarness so all interface methods are satisfied, then
// overrides Binary/Args so harness.Run execs a shell one-liner instead of the
// real CLI. ClaudeHarness.ParseStream tolerates non-JSON lines by emitting them
// as KindText, so the sh output does not need special handling.
type shHarness struct {
	harness.ClaudeHarness
	payload string // JSON body to write to tests.json
	exit    int
	noWrite bool
	stderr  string
	block   bool
}

type filePromptHarness struct{ shHarness }

func (filePromptHarness) GuideFilename() string     { return "AGENTS.md" }
func (filePromptHarness) SystemPromptViaArgs() bool { return false }

type outputSymlinkHarness struct {
	shHarness
	target string
}

func (h outputSymlinkHarness) Args(j harness.Job) []string {
	return []string{"-c", fmt.Sprintf("ln -s %q %q", h.target, j.OutputFile)}
}

type recordingHarness struct {
	shHarness
	model string
}

func (h *recordingHarness) Args(j harness.Job) []string {
	h.model = j.Model
	return h.shHarness.Args(j)
}

func (h shHarness) Binary() string { return "/bin/sh" }

func (h shHarness) Args(j harness.Job) []string {
	var script string
	if !h.noWrite {
		script = fmt.Sprintf("printf %%s %q > %q; ", h.payload, j.OutputFile)
	}
	if h.stderr != "" {
		script += fmt.Sprintf("printf %%s %q >&2; ", h.stderr)
	}
	if h.block {
		script += "sleep 30; "
	}
	script += fmt.Sprintf("exit %d", h.exit)
	return []string{"-c", script}
}

func TestRunSkillWritesFiles(t *testing.T) {
	ws := t.TempDir()
	body := `{"files":[{"path":"test_a.js","content":"// generated\n"}],"notes":"ok"}`
	h := shHarness{payload: body}

	res, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "tests.json", func(harness.Event) {})
	if err != nil {
		t.Fatalf("RunSkill: %v", err)
	}
	var gen GenerateResult
	if err := res.Decode(&gen); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(gen.Files) != 1 || gen.Files[0].Path != "test_a.js" {
		t.Fatalf("output = %+v", gen)
	}
	if gen.Notes != "ok" {
		t.Errorf("notes = %q", gen.Notes)
	}

	// Skill bundle was staged into the backend's discovery dir.
	skillDir := h.SkillDir(ws, "hyrum-generate")
	for _, f := range []string{"SKILL.md", "schema.json"} {
		if _, err := os.Stat(filepath.Join(skillDir, f)); err != nil {
			t.Errorf("%s not staged: %v", f, err)
		}
	}
	// schema.json is also mirrored at the workspace root so ./schema.json in
	// the skill text resolves.
	if _, err := os.Stat(filepath.Join(ws, "schema.json")); err != nil {
		t.Errorf("schema.json not mirrored to workspace root: %v", err)
	}

	out := t.TempDir()
	written, err := WriteFiles(out, gen.Files)
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("written = %v", written)
	}
	b, _ := os.ReadFile(written[0])
	if string(b) != "// generated\n" {
		t.Errorf("file body = %q", b)
	}
}

func TestRunSkillPassesConfiguredModel(t *testing.T) {
	ws := t.TempDir()
	h := &recordingHarness{shHarness: shHarness{payload: `{"files":[]}`}}
	if _, err := RunSkillWithOptions(context.Background(), h, ws, "hyrum-generate", "tests.json", RunOptions{Model: "claude-opus-4-6"}); err != nil {
		t.Fatalf("RunSkillWithOptions: %v", err)
	}
	if h.model != "claude-opus-4-6" {
		t.Fatalf("model = %q", h.model)
	}
}

func TestRunSkillDecodeValidate(t *testing.T) {
	ws := t.TempDir()
	body := `{"verdicts":[{"test":"t","status":"real_break","action":"keep","reasoning":"r"}],"notes":"n"}`
	h := shHarness{payload: body}
	res, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-validate", "verdict.json", func(harness.Event) {})
	if err != nil {
		t.Fatalf("RunSkill: %v", err)
	}
	var v ValidateResult
	if err := res.Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(v.Verdicts) != 1 || v.Verdicts[0].Status != "real_break" || v.Verdicts[0].Action != "keep" {
		t.Errorf("verdicts = %+v", v)
	}
	// Output should also decode into GenerateResult without error, just empty.
	var g GenerateResult
	if err := res.Decode(&g); err != nil || len(g.Files) != 0 {
		t.Errorf("cross-decode: err=%v files=%d", err, len(g.Files))
	}
}

func TestRunSkillInvalidJSON(t *testing.T) {
	ws := t.TempDir()
	h := shHarness{payload: "not json"}
	if _, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "tests.json", nil); err == nil {
		t.Fatal("want error on invalid JSON output")
	}
}

func TestRunSkillBackendFailureWithFreshOutputRecovers(t *testing.T) {
	ws := t.TempDir()
	body := `{"files":[{"path":"test_recovered.js","content":"// recovered\n"}]}`
	h := shHarness{payload: body, exit: 1}
	res, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "tests.json", nil)
	if err != nil {
		t.Fatalf("fresh valid output should survive backend failure: %v", err)
	}
	if res.BackendError == "" {
		t.Fatal("recovered result did not retain backend error")
	}
	var gen GenerateResult
	if err := res.Decode(&gen); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(gen.Files) != 1 || gen.Files[0].Path != "test_recovered.js" {
		t.Fatalf("output = %+v", gen)
	}
}

func TestRunSkillBackendFailureWithoutOutputFails(t *testing.T) {
	ws := t.TempDir()
	h := shHarness{exit: 1, noWrite: true}
	if _, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "tests.json", nil); err == nil {
		t.Fatal("want error when backend fails without output")
	}
}

func TestRunSkillBackendFailureDoesNotReuseStaleOutput(t *testing.T) {
	ws := t.TempDir()
	path := filepath.Join(ws, "tests.json")
	if err := os.WriteFile(path, []byte(`{"files":[{"path":"stale.js","content":"// stale"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := shHarness{exit: 1, noWrite: true}
	if _, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "tests.json", nil); err == nil {
		t.Fatal("want error rather than stale output")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale output was not removed: %v", err)
	}
}

func TestRunSkillBackendFailureWithInvalidOutputFails(t *testing.T) {
	ws := t.TempDir()
	h := shHarness{payload: "not json", exit: 1}
	if _, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "tests.json", nil); err == nil {
		t.Fatal("want error when backend fails with invalid output")
	}
}

func TestRunSkillBackendFailureWithEmptyGenerateOutputFails(t *testing.T) {
	for _, payload := range []string{`{"files":[]}`, `{}`, `null`} {
		t.Run(payload, func(t *testing.T) {
			ws := t.TempDir()
			h := shHarness{payload: payload, exit: 1}
			if _, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "tests.json", nil); err == nil {
				t.Fatal("want error when failed backend writes no generated files")
			}
		})
	}
}

func TestRunSkillBackendFailureWithRenamedEmptyGenerateOutputFails(t *testing.T) {
	ws := t.TempDir()
	h := shHarness{payload: `{"files":[]}`, exit: 1}
	if _, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "generated.json", nil); err == nil {
		t.Fatal("want generate recovery policy to be independent of output filename")
	}
}

func TestRunSkillBackendFailureWithEmptyValidateOutputFails(t *testing.T) {
	for _, payload := range []string{`{"verdicts":[]}`, `{}`, `null`} {
		t.Run(payload, func(t *testing.T) {
			ws := t.TempDir()
			h := shHarness{payload: payload, exit: 1}
			if _, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-validate", "verdict.json", nil); err == nil {
				t.Fatal("want error when failed validation backend writes no verdicts")
			}
		})
	}
}

func TestRunSkillBackendFailureWithValidateVerdictRecovers(t *testing.T) {
	ws := t.TempDir()
	body := `{"verdicts":[{"test":"t","status":"weak","action":"strengthen","reasoning":"r"}]}`
	h := shHarness{payload: body, exit: 1}
	res, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-validate", "verdict.json", nil)
	if err != nil {
		t.Fatalf("usable validate output should survive backend failure: %v", err)
	}
	if res.BackendError == "" {
		t.Fatal("recovered result did not retain backend warning")
	}
	var out ValidateResult
	if err := res.Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Verdicts) != 1 {
		t.Fatalf("verdicts = %+v", out.Verdicts)
	}
}

func TestRunSkillCancelledRunIsFatal(t *testing.T) {
	ws := t.TempDir()
	body := `{"files":[{"path":"test_partial.js","content":"// partial\n"}]}`
	h := shHarness{payload: body, block: true}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if _, err := RunSkillWithEmit(ctx, h, ws, "hyrum-generate", "tests.json", nil); err == nil {
		t.Fatal("want cancellation to remain fatal despite fresh output")
	}
}

func TestRunSkillAccountErrorIsFatal(t *testing.T) {
	ws := t.TempDir()
	body := `{"files":[{"path":"test_partial.js","content":"// partial\n"}]}`
	h := shHarness{payload: body, stderr: "Credit balance is too low", exit: 1}

	_, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "tests.json", nil)
	var accountErr *harness.AccountError
	if !errors.As(err, &accountErr) {
		t.Fatalf("want typed account error, got %v", err)
	}
}

func TestPrepareOutputRejectsSymlinkedDirectory(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.json")
	if err := os.WriteFile(victim, []byte("important"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "sub")); err != nil {
		t.Fatal(err)
	}

	root, err := safefs.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := prepareOutput(root, filepath.Join("sub", "victim.json")); err == nil {
		t.Fatal("want nested output path rejected")
	}
	b, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("outside file was removed: %v", err)
	}
	if !strings.EqualFold(string(b), "important") {
		t.Fatalf("outside file changed: %q", b)
	}
}

func TestRunSkillMissingOutput(t *testing.T) {
	ws := t.TempDir()
	h := shHarness{noWrite: true}
	if _, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "tests.json", nil); err == nil {
		t.Fatal("want error when tests.json not written")
	}
}

func TestRunSkillReplacesPlantedWorkspaceSymlinks(t *testing.T) {
	ws := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := filePromptHarness{shHarness{payload: `{"files":[{"path":"test.js","content":"ok"}]}`}}
	skillDir := h.SkillDir(ws, "hyrum-generate")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(ws, "AGENTS.md"),
		filepath.Join(ws, "schema.json"),
		filepath.Join(skillDir, "SKILL.md"),
	} {
		if err := os.Symlink(victim, path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RunSkillWithEmit(t.Context(), h, ws, "hyrum-generate", "tests.json", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Fatalf("guide staging changed external file to %q", got)
	}
	for _, path := range []string{filepath.Join(ws, "AGENTS.md"), filepath.Join(ws, "schema.json"), filepath.Join(skillDir, "SKILL.md")} {
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s was not replaced with a regular file: %v, %v", path, info, err)
		}
	}
}

func TestRunSkillRejectsModelOutputSymlink(t *testing.T) {
	ws := t.TempDir()
	victim := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(victim, []byte(`{"files":[{"path":"stolen.js","content":"secret"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := outputSymlinkHarness{shHarness: shHarness{}, target: victim}
	if _, err := RunSkillWithEmit(t.Context(), h, ws, "hyrum-generate", "tests.json", nil); err == nil {
		t.Fatal("model-produced output symlink was accepted")
	}
}

func TestWriteFilesRejectsEscape(t *testing.T) {
	out := t.TempDir()
	for _, p := range []string{"../evil", "/etc/passwd", ""} {
		if _, err := WriteFiles(out, []GeneratedFile{{Path: p, Content: "x"}}); err == nil {
			t.Errorf("path %q: want error", p)
		}
	}
}

func TestWriteFilesRejectsFinalSymlinkEscape(t *testing.T) {
	out := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.js")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(out, "test.js")); err != nil {
		t.Fatal(err)
	}

	if _, err := WriteFiles(out, []GeneratedFile{{Path: "test.js", Content: "overwritten"}}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "original" {
		t.Fatalf("outside file changed to %q", body)
	}
	if info, err := os.Lstat(filepath.Join(out, "test.js")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("output was not replaced with a regular file: %v, %v", info, err)
	}
}

func TestWriteFilesUnderRejectsDirectorySymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "dep")); err != nil {
		t.Fatal(err)
	}

	files := []GeneratedFile{{Path: "test.js", Content: "generated"}}
	if _, err := WriteFilesUnder(root, filepath.Join("dep", "from_target"), files); err == nil {
		t.Fatal("WriteFilesUnder followed a directory symlink outside the output root")
	}
	if _, err := os.Stat(filepath.Join(outside, "from_target", "test.js")); !os.IsNotExist(err) {
		t.Fatalf("file written outside output root: %v", err)
	}
}

func TestWriteFilesUnderWritesNestedOutput(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join("dep", "from_target")
	files := []GeneratedFile{{Path: filepath.Join("nested", "test.js"), Content: "generated"}}

	written, err := WriteFilesUnder(root, dir, files)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, dir, "nested", "test.js")
	if len(written) != 1 || written[0] != want {
		t.Fatalf("written = %v, want [%s]", written, want)
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "generated" {
		t.Fatalf("body = %q", body)
	}
}

func TestReplaceFilesUnderRemovesObsoleteFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join("dep", "from_target")
	if _, err := WriteFilesUnder(root, dir, []GeneratedFile{
		{Path: "obsolete.test.js", Content: "old"},
		{Path: "kept.test.js", Content: "old"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ReplaceFilesUnder(root, dir, []GeneratedFile{{Path: "kept.test.js", Content: "new"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, dir, "obsolete.test.js")); !os.IsNotExist(err) {
		t.Fatalf("obsolete file remains: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, dir, "kept.test.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("kept file = %q, want new", got)
	}
}

func TestReplaceFilesUnderValidatesBeforeRemovingSuite(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join("dep", "from_target")
	if _, err := WriteFilesUnder(root, dir, []GeneratedFile{{Path: "existing.test.js", Content: "old"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := ReplaceFilesUnder(root, dir, []GeneratedFile{{Path: "../escape", Content: "bad"}}); err == nil {
		t.Fatal("ReplaceFilesUnder accepted an escaping path")
	}
	if _, err := os.Stat(filepath.Join(root, dir, "existing.test.js")); err != nil {
		t.Fatalf("existing suite was removed before validation: %v", err)
	}
}

func TestReplaceFilesUnderReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	dir := filepath.Join("dep", "from_target")
	if err := os.Mkdir(filepath.Join(root, "dep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, dir)); err != nil {
		t.Fatal(err)
	}

	if _, err := ReplaceFilesUnder(root, dir, []GeneratedFile{{Path: "current.test.js", Content: "new"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, dir, "current.test.js")); err != nil {
		t.Fatalf("replacement suite was not written: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(outside, "keep"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside" {
		t.Fatalf("outside file changed to %q", got)
	}
}
