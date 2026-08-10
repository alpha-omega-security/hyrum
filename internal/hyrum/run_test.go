package hyrum

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/alpha-omega-security/harness"
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
}

func (h shHarness) Binary() string { return "/bin/sh" }

func (h shHarness) Args(j harness.Job) []string {
	var script string
	if !h.noWrite {
		script = fmt.Sprintf("printf %%s %q > %q; ", h.payload, j.OutputFile)
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
	if len(res.Output.Files) != 1 || res.Output.Files[0].Path != "test_a.js" {
		t.Fatalf("output = %+v", res.Output)
	}
	if res.Output.Notes != "ok" {
		t.Errorf("notes = %q", res.Output.Notes)
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
	written, err := WriteFiles(out, res.Output.Files)
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

func TestRunSkillBackendFailure(t *testing.T) {
	ws := t.TempDir()
	h := shHarness{payload: `{"files":[]}`, exit: 1}
	if _, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "tests.json", nil); err == nil {
		t.Fatal("want error on non-zero exit")
	}
}

func TestRunSkillMissingOutput(t *testing.T) {
	ws := t.TempDir()
	h := shHarness{noWrite: true}
	if _, err := RunSkillWithEmit(context.Background(), h, ws, "hyrum-generate", "tests.json", nil); err == nil {
		t.Fatal("want error when tests.json not written")
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
