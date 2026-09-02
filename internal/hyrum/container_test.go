package hyrum

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alpha-omega-security/harness"
	harnesscontainer "github.com/alpha-omega-security/harness/container"
)

type eventHarness struct{ shHarness }

func (eventHarness) ParseStream(_ io.Reader, emit func(harness.Event)) {
	emit(harness.Event{Kind: harness.KindResult, CostUSD: 0.25, Turns: 3})
	emit(harness.Event{Kind: harness.KindSession, SessionID: "session-1"})
}

func TestContainerRunArgs(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	r := ContainerRunner{TargetPath: "/repo", DependencyPath: "/dependency"}
	r.Image = "img:tag"
	args, _ := runContainerAndReadArgs(t, r, shHarness{payload: `{"files":[]}`})
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"run --rm",
		"--cap-drop ALL",
		"--security-opt no-new-privileges",
		"--read-only",
		"-e HOME=/tmp",
		"--tmpfs /tmp:rw,exec,nosuid,size=256m",
		"-w /work",
		"-v /repo:/work/target:ro",
		"-v /dependency:/work/dep:ro",
		"-e ANTHROPIC_API_KEY",
		"--network bridge",
		"-- img:tag /bin/sh",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "sk-test") {
		t.Fatalf("credential value exposed in runtime argv: %q", joined)
	}
}

func TestDefaultRunnerImageIsDigestPinned(t *testing.T) {
	if !strings.Contains(DefaultRunnerImage, "@sha256:") || strings.Contains(DefaultRunnerImage, ":latest") {
		t.Fatalf("default runner image is mutable: %q", DefaultRunnerImage)
	}
}

func TestDetectContainerRuntimeRejectsUnknownRuntime(t *testing.T) {
	if _, err := DetectContainerRuntime("unknown"); err == nil || !strings.Contains(err.Error(), `"unknown"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestContainerRunArgsNoTarget(t *testing.T) {
	r := ContainerRunner{}
	r.Image = "img"
	args, _ := runContainerAndReadArgs(t, r, shHarness{payload: `{"files":[]}`})
	for _, a := range args {
		if strings.Contains(a, "/work/target") {
			t.Errorf("target mount present without TargetPath: %v", args)
		}
	}
}

func TestContainerRunnerPreservesEvents(t *testing.T) {
	var kinds []string
	runner := ContainerRunner{
		Image: "img",
		Emit: func(event harness.Event) {
			kinds = append(kinds, event.Kind)
		},
	}
	_, result := runContainerAndReadArgs(t, runner, eventHarness{shHarness{payload: `{"files":[]}`}})
	if result.CostUSD != 0.25 || result.Turns != 3 || result.SessionID != "session-1" {
		t.Fatalf("result = %+v", result)
	}
	if strings.Join(kinds, ",") != "result,session" {
		t.Fatalf("event kinds = %v", kinds)
	}
}

func TestContainerRunnerDockerIntegration(t *testing.T) {
	image := os.Getenv("HYRUM_TEST_RUNNER_IMAGE")
	if image == "" {
		t.Skip("HYRUM_TEST_RUNNER_IMAGE is not set")
	}
	runtime, err := DetectContainerRuntime("docker")
	if err != nil {
		t.Skip(err)
	}
	ws := t.TempDir()
	runner := ContainerRunner{Runtime: runtime, Image: image}
	result, err := runner.RunSkill(t.Context(), shHarness{payload: `{"files":[]}`}, ws, "hyrum-generate", "tests.json", RunOptions{})
	if err != nil {
		t.Fatalf("RunSkill: %v", err)
	}
	if string(result.Output) != `{"files":[]}` {
		t.Fatalf("output = %s", result.Output)
	}
}

func runContainerAndReadArgs(t *testing.T, runner ContainerRunner, h harness.Harness) ([]string, *RunResult) {
	t.Helper()
	ws := t.TempDir()
	argsPath := filepath.Join(t.TempDir(), "args")
	t.Setenv("HYRUM_TEST_ARGS", argsPath)
	t.Setenv("HYRUM_TEST_OUTPUT", filepath.Join(ws, "tests.json"))
	runtime := filepath.Join(t.TempDir(), "fake-container-runtime")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$HYRUM_TEST_ARGS"
printf '%s' '{"files":[]}' > "$HYRUM_TEST_OUTPUT"
`
	if err := os.WriteFile(runtime, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner.Runtime = harnesscontainer.Runtime{Bin: runtime}
	result, err := runner.RunSkill(t.Context(), h, ws, "hyrum-generate", "tests.json", RunOptions{})
	if err != nil {
		t.Fatalf("RunSkill: %v", err)
	}
	b, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n"), result
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

	runner := ContainerRunner{Runtime: harnesscontainer.Runtime{Bin: runtime}, Image: "test-image"}
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

	runner := ContainerRunner{Runtime: harnesscontainer.Runtime{Bin: runtime}, Image: "test-image"}
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

	runner := ContainerRunner{Runtime: harnesscontainer.Runtime{Bin: runtime}, Image: "test-image"}
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

	runner := ContainerRunner{Runtime: harnesscontainer.Runtime{Bin: runtime}, Image: "test-image"}
	_, err := runner.RunSkill(context.Background(), shHarness{}, ws, "hyrum-generate", "tests.json", RunOptions{})
	var accountErr *harness.AccountError
	if !errors.As(err, &accountErr) {
		t.Fatalf("want typed account error, got %v", err)
	}
}

func TestContainerRunnerPassesConfiguredModel(t *testing.T) {
	ws := t.TempDir()
	h := &recordingHarness{shHarness: shHarness{payload: `{"files":[]}`}}
	r := ContainerRunner{Runtime: harnesscontainer.Runtime{Bin: filepath.Join(t.TempDir(), "missing-runtime")}}
	_, err := r.RunSkill(context.Background(), h, ws, "hyrum-generate", "tests.json", RunOptions{Model: "claude-opus-4-6"})
	if err == nil {
		t.Fatal("RunSkill succeeded with missing runtime")
	}
	if h.model != "claude-opus-4-6" {
		t.Fatalf("model = %q", h.model)
	}
}
