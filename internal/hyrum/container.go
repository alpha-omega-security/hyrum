package hyrum

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/alpha-omega-security/harness"
	harnesscontainer "github.com/alpha-omega-security/harness/container"
	"github.com/alpha-omega-security/hyrum/internal/safefs"
)

// DefaultRunnerImage bundles the harness backends (claude, codex, opencode),
// brief, git-pkgs, git, and node/python/go toolchains. It is built from
// alpha-omega-security/scrutineer/Dockerfile.runner and published per release.
const DefaultRunnerImage = "ghcr.io/alpha-omega-security/scrutineer-runner@sha256:c3b6361ccf7a8f440f8ccdf13e88ad996fb3b17846bac032e88a9b405b707baa"

// Runner runs one skill and returns its parsed output. HostRunner and
// ContainerRunner both satisfy it so the pipeline can switch on a flag.
type Runner interface {
	RunSkill(ctx context.Context, h harness.Harness, ws, name, outputFile string, opts RunOptions) (*RunResult, error)
}

// HostRunner runs the backend directly on the host via harness.Run. It is the
// default and requires the backend binary on PATH.
type HostRunner struct{}

func (HostRunner) RunSkill(ctx context.Context, h harness.Harness, ws, name, out string, opts RunOptions) (*RunResult, error) {
	return RunSkillWithOptions(ctx, h, ws, name, out, opts)
}

// ContainerRunner runs the backend inside an ephemeral container with a fresh
// HOME, dropped capabilities, and the workspace bind-mounted at /work. The
// target repository is mounted read-only at /work/target so agent-directive
// files it may contain cannot be modified or auto-loaded from a writable path,
// and the user's real checkout is never touched.
type ContainerRunner struct {
	// Image is the runner image; empty uses DefaultRunnerImage.
	Image string
	// Runtime is the detected container engine; the zero value uses docker.
	Runtime harnesscontainer.Runtime
	// TargetPath is the host path to mount read-only at /work/target. When
	// set, any existing ws/target symlink is removed before the run so the
	// mount point is clean.
	TargetPath string
	// DependencyPath is the host dependency checkout mounted read-only at
	// /work/dep. It lives outside the writable agent workspace.
	DependencyPath string
	// Emit receives parsed backend events; nil uses defaultEmit.
	Emit func(harness.Event)
}

// DetectContainerRuntime verifies the requested container engine before use.
func DetectContainerRuntime(prefer string) (harnesscontainer.Runtime, error) {
	runtime, ok := harnesscontainer.DetectRuntime(prefer)
	if ok {
		return runtime, nil
	}
	if prefer == "" {
		prefer = "docker"
	}
	return harnesscontainer.Runtime{}, fmt.Errorf("container runtime %q is unavailable", prefer)
}

func (r ContainerRunner) RunSkill(ctx context.Context, h harness.Harness, ws, name, outputFile string, opts RunOptions) (*RunResult, error) {
	absWork, err := filepath.Abs(ws)
	if err != nil {
		return nil, err
	}
	workspace, err := safefs.Open(absWork)
	if err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	defer func() { _ = workspace.Close() }()
	if err := stageSkillFiles(workspace, h, ws, name); err != nil {
		return nil, err
	}

	job := harness.Job{
		Workspace:    absWork,
		SrcDir:       TargetSubdir,
		SkillName:    name,
		OutputFile:   outputFile,
		SystemPrompt: headlessSystemPrompt,
		Model:        opts.Model,
		MaxTurns:     opts.MaxTurns,
	}
	if err := stageSystemPrompt(workspace, h, &job); err != nil {
		return nil, err
	}
	outputName, err := prepareOutput(workspace, outputFile)
	if err != nil {
		return nil, err
	}
	if err := r.prepareMountPoints(workspace); err != nil {
		return nil, err
	}
	model := opts.Model
	if model == "" {
		if defs := h.DefaultModels(); len(defs) > 0 {
			model = defs[0].ID
		}
	}
	emit := r.Emit
	if emit == nil {
		emit = defaultEmit
	}
	res := &RunResult{}
	containerRunner, err := r.runner()
	if err != nil {
		return nil, err
	}
	runErr := containerRunner.Run(ctx, h, job, func(e harness.Event) {
		recordRunEvent(res, model, e)
		emit(e)
	})
	return finishRun(ctx, res, workspace, outputName, name, outputFile, runErr)
}

func (r ContainerRunner) runner() (harnesscontainer.Runner, error) {
	image := r.Image
	if image == "" {
		image = DefaultRunnerImage
	}
	mounts := make([]harnesscontainer.Mount, 0, 2)
	if r.TargetPath != "" {
		target, err := filepath.Abs(r.TargetPath)
		if err != nil {
			return harnesscontainer.Runner{}, fmt.Errorf("target mount: %w", err)
		}
		mounts = append(mounts, harnesscontainer.Mount{
			Host: target, Container: "/work/target", ReadOnly: true,
		})
	}
	if r.DependencyPath != "" {
		dependency, err := filepath.Abs(r.DependencyPath)
		if err != nil {
			return harnesscontainer.Runner{}, fmt.Errorf("dependency mount: %w", err)
		}
		mounts = append(mounts, harnesscontainer.Mount{
			Host: dependency, Container: "/work/dep", ReadOnly: true,
		})
	}
	return harnesscontainer.Runner{
		Runtime:  r.Runtime,
		Image:    image,
		Mounts:   mounts,
		Network:  "bridge", // Model backends need network access.
		ReadOnly: true,
	}, nil
}

func (r ContainerRunner) prepareMountPoints(workspace *safefs.Root) error {
	for _, mount := range []struct {
		path    string
		enabled bool
	}{
		{path: TargetSubdir, enabled: r.TargetPath != ""},
		{path: "dep", enabled: r.DependencyPath != ""},
	} {
		if !mount.enabled {
			continue
		}
		if err := workspace.RemoveAll(mount.path); err != nil {
			return err
		}
		if err := workspace.MkdirAll(mount.path, 0o755); err != nil {
			return err
		}
	}
	return nil
}
