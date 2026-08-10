package hyrum

import (
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
