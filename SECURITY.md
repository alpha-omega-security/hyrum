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
containing `..` (`internal/hyrum/run.go:WriteFiles`). The dependency clone is
stripped of files that agent CLIs auto-load as instructions (`CLAUDE.md`,
`AGENTS.md`, `.claude/`, `.cursor/`, and similar;
`internal/hyrum/strip.go:StripAgentDirectives`) before any skill reads it. A
workspace-level project-instructions file states that the run is
non-interactive so a global user rule of "stop and ask on error" does not
leave the process waiting on absent input. `git` and package-manager
subprocesses receive argv arrays; dependency names and URLs are never
interpolated into a shell string.

### Known gaps

`gen` symlinks the target repository into the workspace rather than copying
it, so agent-directive files in the target are not stripped (stripping would
modify the user's checkout). `corpus` clones targets into its own working
directory and could strip them but currently does not. Registry metadata
(homepage, description, repository URL) is written into `context.json` for the
skill to read; a package with adversarial metadata could place instruction-like
text there. Each `SKILL.md` states that workspace content is data rather than
instructions, which is advisory.

Running the backend inside a container with a fresh HOME and a read-only
target mount, as
[scrutineer](https://github.com/alpha-omega-security/scrutineer) does, closes
these gaps by construction. Until that lands, run `--run` on repositories and
dependencies you have reason to trust.
