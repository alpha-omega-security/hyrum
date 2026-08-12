package hyrum

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alpha-omega-security/harness"
)

func TestContainerRunArgs(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	r := ContainerRunner{TargetPath: "/repo"}
	h := harness.ClaudeHarness{}
	args := r.runArgs("/tmp/ws", "img:tag", h)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"run --rm",
		"--cap-drop ALL",
		"--security-opt no-new-privileges",
		"-e HOME=/tmp",
		"--tmpfs /tmp:rw,nosuid,size=256m",
		"-v /tmp/ws:/work",
		"-w /work",
		"-v /repo:/work/target:ro",
		"-e ANTHROPIC_API_KEY=sk-test",
		"-- img:tag",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
	// Image is last, after --.
	if args[len(args)-1] != "img:tag" || args[len(args)-2] != "--" {
		t.Errorf("image not terminal: %v", args[len(args)-3:])
	}
}

func TestContainerRunArgsNoTarget(t *testing.T) {
	r := ContainerRunner{}
	args := r.runArgs("/tmp/ws", "img", harness.ClaudeHarness{})
	for _, a := range args {
		if strings.Contains(a, "/work/target:ro") {
			t.Errorf("target mount present without TargetPath: %v", args)
		}
	}
}

func TestHostRunnerSatisfiesRunner(t *testing.T) {
	var _ Runner = HostRunner{}
	var _ Runner = ContainerRunner{}
}

func TestContainerRunnerBackendFailureWithFreshOutputRecovers(t *testing.T) {
	ws := t.TempDir()
	outputPath := filepath.Join(ws, "tests.json")
	t.Setenv("HYRUM_TEST_OUTPUT", outputPath)

	runtime := filepath.Join(t.TempDir(), "fake-container-runtime")
	script := `#!/bin/sh
printf '%s' '{"files":[{"path":"test_recovered.js","content":"// recovered\n"}]}' > "$HYRUM_TEST_OUTPUT"
exit 1
`
	if err := os.WriteFile(runtime, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	runner := ContainerRunner{Runtime: runtime, Image: "test-image"}
	res, err := runner.RunSkill(context.Background(), shHarness{}, ws, "hyrum-generate", "tests.json", RunOptions{})
	if err != nil {
		t.Fatalf("fresh valid output should survive container failure: %v", err)
	}
	if res.BackendError == "" {
		t.Fatal("recovered result did not retain backend error")
	}
	var gen GenerateResult
	if err := res.Decode(&gen); err != nil {
		t.Fatal(err)
	}
	if len(gen.Files) != 1 || gen.Files[0].Path != "test_recovered.js" {
		t.Fatalf("output = %+v", gen)
	}
}

func TestContainerRunnerBackendFailureDoesNotReuseStaleOutput(t *testing.T) {
	ws := t.TempDir()
	outputPath := filepath.Join(ws, "tests.json")
	if err := os.WriteFile(outputPath, []byte(`{"files":[{"path":"stale.js","content":"// stale"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime := filepath.Join(t.TempDir(), "fake-container-runtime")
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	runner := ContainerRunner{Runtime: runtime, Image: "test-image"}
	if _, err := runner.RunSkill(context.Background(), shHarness{}, ws, "hyrum-generate", "tests.json", RunOptions{}); err == nil {
		t.Fatal("want error rather than stale container output")
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("stale output was not removed: %v", err)
	}
}

func TestContainerRunnerRecoveryDoesNotRetainStderr(t *testing.T) {
	ws := t.TempDir()
	outputPath := filepath.Join(ws, "tests.json")
	t.Setenv("HYRUM_TEST_OUTPUT", outputPath)

	runtime := filepath.Join(t.TempDir(), "fake-container-runtime")
	script := `#!/bin/sh
printf '%s' '{"files":[{"path":"test_recovered.js","content":"// recovered\n"}]}' > "$HYRUM_TEST_OUTPUT"
printf '%s' 'token sk-ant-secret-value-1234 rejected' >&2
exit 1
`
	if err := os.WriteFile(runtime, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	runner := ContainerRunner{Runtime: runtime, Image: "test-image"}
	res, err := runner.RunSkill(context.Background(), shHarness{}, ws, "hyrum-generate", "tests.json", RunOptions{})
	if err != nil {
		t.Fatalf("fresh valid output should survive container failure: %v", err)
	}
	if strings.Contains(res.BackendError, "sk-ant-secret-value-1234") {
		t.Fatalf("backend warning retained stderr secret: %q", res.BackendError)
	}
	if len(res.BackendError) > 256 {
		t.Fatalf("backend warning is unbounded: %d bytes", len(res.BackendError))
	}
}

func TestContainerRunnerAccountErrorIsFatal(t *testing.T) {
	ws := t.TempDir()
	outputPath := filepath.Join(ws, "tests.json")
	t.Setenv("HYRUM_TEST_OUTPUT", outputPath)

	runtime := filepath.Join(t.TempDir(), "fake-container-runtime")
	script := `#!/bin/sh
printf '%s' '{"files":[{"path":"test_partial.js","content":"// partial\n"}]}' > "$HYRUM_TEST_OUTPUT"
printf '%s' 'Credit balance is too low' >&2
exit 1
`
	if err := os.WriteFile(runtime, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	runner := ContainerRunner{Runtime: runtime, Image: "test-image"}
	_, err := runner.RunSkill(context.Background(), shHarness{}, ws, "hyrum-generate", "tests.json", RunOptions{})
	var accountErr *harness.AccountError
	if !errors.As(err, &accountErr) {
		t.Fatalf("want typed account error, got %v", err)
	}
}

func TestContainerRunnerPassesConfiguredModel(t *testing.T) {
	ws := t.TempDir()
	h := &recordingHarness{shHarness: shHarness{payload: `{"files":[]}`}}
	r := ContainerRunner{Runtime: filepath.Join(t.TempDir(), "missing-runtime")}
	_, err := r.RunSkill(context.Background(), h, ws, "hyrum-generate", "tests.json", RunOptions{Model: "claude-opus-4-6"})
	if err == nil {
		t.Fatal("RunSkill succeeded with missing runtime")
	}
	if h.model != "claude-opus-4-6" {
		t.Fatalf("model = %q", h.model)
	}
}
