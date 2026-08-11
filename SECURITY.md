# Security

## Reporting

Report vulnerabilities via GitHub's private vulnerability reporting on this
repository, or by email to security@alpha-omega.dev.

## Threat model

`hyrum gen --run` and `hyrum corpus --run` invoke an LLM backend against a
workspace containing source code from three origins: the target repository
(user-supplied path or corpus-cloned URL), a clone of the dependency's source
repository (URL from the package registry), and metadata fetched from the
registry and OSV. All three can contain text crafted to influence the LLM.

### What is protected

Generated file paths from the LLM are rejected if absolute, empty, or
containing `..` (`internal/hyrum/run.go:WriteFiles`). The dependency clone,
and each dependent clone in `corpus`, is stripped of files that agent CLIs
auto-load as instructions (`CLAUDE.md`, `AGENTS.md`, `.claude/`, `.cursor/`,
and similar; `harness.StripDirectives`) before any skill reads it. A
workspace-level project-instructions file states that the run is
non-interactive so a global user rule of "stop and ask on error" does not
leave the process waiting on absent input. `git` and package-manager
subprocesses receive argv arrays; dependency names and URLs are never
interpolated into a shell string.

### Known gaps

`gen` symlinks the target repository into the workspace rather than copying
it, so agent-directive files in the target are not stripped (stripping would
modify the user's checkout). Registry metadata
(homepage, description, repository URL) is written into `context.json` for the
skill to read; a package with adversarial metadata could place instruction-like
text there. The statement in each `SKILL.md` that workspace content is data
rather than instructions is advisory and depends on the backend honouring it.

### `--container`

Passing `--container default` (or an image name) to `gen` or `corpus` runs the
backend inside an ephemeral container with `--cap-drop ALL`,
`--security-opt no-new-privileges`, a non-root user, `HOME=/tmp` on a tmpfs,
the workspace bind-mounted at `/work`, and the target repository bind-mounted
read-only at `/work/target`. That removes access to the user's global agent
configuration and prevents any write to the target checkout, closing the gaps
above except the registry-metadata one (which is bounded to text inside a JSON
value the backend reads). The default image is
`ghcr.io/alpha-omega-security/scrutineer-runner`, which bundles the harness
backends and the git-pkgs tools. Network egress is not restricted in this
mode; [scrutineer](https://github.com/alpha-omega-security/scrutineer)'s
runner adds an allowlist proxy for that, and extracting its container runner
into harness for shared use is tracked upstream.

Without `--container`, restrict `--run` to repositories and dependencies you
have reason to trust.
