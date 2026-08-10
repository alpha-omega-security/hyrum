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
	if res[1].Version != "8.21.3" || res[1].Fail == 0 {
		t.Errorf("latest expected at least one fail: %+v", res[1])
	}
}
