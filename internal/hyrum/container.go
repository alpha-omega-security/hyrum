package hyrum

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/alpha-omega-security/harness"
	"github.com/alpha-omega-security/hyrum/skills"
)

// DefaultRunnerImage bundles the harness backends (claude, codex, opencode),
// brief, git-pkgs, git, and node/python/go toolchains. It is built from
// alpha-omega-security/scrutineer/Dockerfile.runner and published per release.
const DefaultRunnerImage = "ghcr.io/alpha-omega-security/scrutineer-runner:latest"

// Runner runs one skill and returns its parsed output. HostRunner and
// ContainerRunner both satisfy it so the pipeline can switch on a flag.
type Runner interface {
	RunSkill(ctx context.Context, h harness.Harness, ws, name, outputFile string) (*RunResult, error)
}

// HostRunner runs the backend directly on the host via harness.Run. It is the
// default and requires the backend binary on PATH.
type HostRunner struct{}

func (HostRunner) RunSkill(ctx context.Context, h harness.Harness, ws, name, out string) (*RunResult, error) {
	return RunSkill(ctx, h, ws, name, out)
}

// ContainerRunner runs the backend inside an ephemeral container with a fresh
// HOME, dropped capabilities, and the workspace bind-mounted at /work. The
// target repository is mounted read-only at /work/target so agent-directive
// files it may contain cannot be modified or auto-loaded from a writable path,
// and the user's real checkout is never touched.
type ContainerRunner struct {
	// Image is the runner image; empty uses DefaultRunnerImage.
	Image string
	// Runtime is the container CLI (docker, podman); empty uses docker.
	Runtime string
	// TargetPath is the host path to mount read-only at /work/target. When
	// set, any existing ws/target symlink is removed before the run so the
	// mount point is clean.
	TargetPath string
	// Emit receives parsed backend events; nil uses defaultEmit.
	Emit func(harness.Event)
}

func (r ContainerRunner) RunSkill(ctx context.Context, h harness.Harness, ws, name, outputFile string) (*RunResult, error) {
	skillDir, err := skills.Stage(h, ws, name)
	if err != nil {
		return nil, fmt.Errorf("stage skill: %w", err)
	}
	if b, err := os.ReadFile(filepath.Join(skillDir, "schema.json")); err == nil {
		_ = os.WriteFile(filepath.Join(ws, "schema.json"), b, 0o644)
	}
	if err := harness.WriteSystemPrompt(h, harness.Job{
		Workspace:    ws,
		SystemPrompt: headlessSystemPrompt,
	}); err != nil {
		return nil, err
	}

	absWork, err := filepath.Abs(ws)
	if err != nil {
		return nil, err
	}
	if r.TargetPath != "" {
		_ = os.Remove(filepath.Join(absWork, "target"))
		if err := os.MkdirAll(filepath.Join(absWork, "target"), 0o755); err != nil {
			return nil, err
		}
	}

	job := harness.Job{
		Workspace:    "/work",
		SrcDir:       "target",
		SkillName:    name,
		OutputFile:   outputFile,
		SystemPrompt: headlessSystemPrompt,
	}

	runtime := r.Runtime
	if runtime == "" {
		runtime = "docker"
	}
	image := r.Image
	if image == "" {
		image = DefaultRunnerImage
	}

	args := r.runArgs(absWork, image, h)
	args = append(args, h.Binary())
	args = append(args, h.Args(job)...)

	cmd := exec.CommandContext(ctx, runtime, args...)
	cmd.Env = os.Environ()
	pr, pw := io.Pipe()
	var stderr strings.Builder
	cmd.Stdout = pw
	cmd.Stderr = io.MultiWriter(pw, &stderr)

	model := ""
	if defs := h.DefaultModels(); len(defs) > 0 {
		model = defs[0].ID
	}
	emit := r.Emit
	if emit == nil {
		emit = defaultEmit
	}
	res := &RunResult{}
	done := make(chan struct{})
	go func() {
		h.ParseStream(pr, func(e harness.Event) {
			switch e.Kind {
			case harness.KindResult:
				cost := e.CostUSD
				if cost == 0 && model != "" {
					cost = harness.CostFromUsage(model, e.Usage)
				}
				res.CostUSD = cost
				res.Turns = e.Turns
			case harness.KindSession:
				res.SessionID = e.SessionID
			}
			emit(e)
		})
		close(done)
	}()

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		<-done
		return nil, fmt.Errorf("start %s: %w", runtime, err)
	}
	waitErr := cmd.Wait()
	_ = pw.Close()
	<-done
	if waitErr != nil {
		if detail := h.AccountErrorText(stderr.String()); detail != "" {
			return nil, fmt.Errorf("%s: %s", h.Binary(), detail)
		}
		return nil, fmt.Errorf("%s run: %w: %s", runtime, waitErr, strings.TrimSpace(stderr.String()))
	}

	b, err := os.ReadFile(filepath.Join(absWork, outputFile))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (skill did not write output)", outputFile, err)
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("%s is not valid JSON", outputFile)
	}
	res.Output = b
	return res, nil
}

// runArgs builds the container-run argv up to and including the image name.
// The workspace is mounted read-write at /work so the skill can write its
// output file; the target is mounted read-only so its contents cannot be
// modified and any agent-directive files it contains are inert on a writable
// path. HOME is a tmpfs so the user's ~/.codex, ~/.claude etc. are absent.
func (r ContainerRunner) runArgs(absWork, image string, h harness.Harness) []string {
	args := []string{
		"run", "--rm",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-e", "HOME=/tmp",
		"--tmpfs", "/tmp:rw,nosuid,size=256m",
		"-v", absWork + ":/work",
		"-w", "/work",
	}
	if r.TargetPath != "" {
		abs, _ := filepath.Abs(r.TargetPath)
		args = append(args, "-v", abs+":/work/target:ro")
	}
	// Credential and telemetry-suppression env for this backend. harness.Env
	// returns bare keys for pass-through; expand them from the host env so
	// the value is set inside the container.
	for _, e := range h.Env("") {
		if !strings.ContainsRune(e, '=') {
			e = e + "=" + os.Getenv(e)
		}
		args = append(args, "-e", e)
	}
	return append(args, "--", image)
}
