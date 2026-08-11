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
