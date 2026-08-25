package hyrum

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/git-pkgs/managers"
	"github.com/git-pkgs/managers/definitions"
)

// TestVerifyMatrixNPM exercises the full scratch-dir + init + add + node --test
// path against real npm and the ws test file generated in an earlier session
// run. It is skipped under -short and when npm or the fixture are absent so CI
// stays hermetic.
func TestVerifyMatrixNPM(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	body, err := os.ReadFile("/tmp/hyrum/npm/ws/tests.json")
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	var out struct{ Files []GeneratedFile }
	if err := json.Unmarshal(body, &out); err != nil || len(out.Files) == 0 {
		t.Skipf("fixture unparseable: %v", err)
	}

	scratch := t.TempDir()
	defs, _ := definitions.LoadEmbedded()
	det := managers.NewDetector(managers.NewTranslator(), managers.NewExecRunner())
	for _, d := range defs {
		det.Register(d)
	}
	mgr, err := det.Detect(scratch, managers.DetectOptions{Manager: "npm"})
	if err != nil {
		t.Fatalf("detect npm on empty dir: %v", err)
	}

	tc := func(_ string, files []string) []string { return append([]string{"node", "--test"}, files...) }
	res := VerifyMatrix(context.Background(), mgr, tc, scratch, "ws", out.Files, []string{"7.4.2", "8.21.3"})

	if len(res) != 2 {
		t.Fatalf("results = %d, want 2: %+v", len(res), res)
	}
	for _, r := range res {
		t.Logf("%s: pass=%d fail=%d failed=%v err=%s", r.Version, r.Pass, r.Fail, r.Failed, r.Error)
	}
	if res[0].Version != "7.4.2" || res[0].Fail != 0 || res[0].Pass == 0 {
		t.Errorf("baseline: %+v", res[0])
	}
	if res[0].Output != "" {
		t.Errorf("baseline all-pass should not retain output (%d bytes)", len(res[0].Output))
	}
	if res[1].Version != "8.21.3" || res[1].Fail == 0 {
		t.Errorf("latest expected at least one fail: %+v", res[1])
	}
	if res[1].Output == "" {
		t.Error("latest with failures should retain runner output for validate")
	}
}

// TestVerifyMatrixPyPI exercises virtualenv creation, dependency replacement,
// and pytest with two published versions. It is opt-in because it downloads
// packages from PyPI.
func TestVerifyMatrixPyPI(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	if os.Getenv("HYRUM_TEST_PYPI") == "" {
		t.Skip("set HYRUM_TEST_PYPI=1 to run the PyPI integration test")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}

	working := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(working); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	scratch := "verify"
	files := []GeneratedFile{{
		Path: "test_idna.py",
		Content: `import idna

def test_idna_encode():
    assert idna.encode("example.com") == b"example.com"
`,
	}}
	versions := []string{"3.6", "3.7"}
	results := VerifyMatrix(
		t.Context(),
		NewPythonVenvManager(scratch),
		PythonVenvTestCommand(scratch),
		scratch,
		"idna",
		files,
		versions,
	)

	if len(results) != len(versions) {
		t.Fatalf("results = %d, want %d: %+v", len(results), len(versions), results)
	}
	for i, result := range results {
		if result.Version != versions[i] || result.Error != "" || result.Fail != 0 || result.Pass == 0 {
			t.Errorf("version %s: %+v", versions[i], result)
		}
	}
}
