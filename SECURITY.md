# Security policy

## Reporting a vulnerability

Please report security issues through GitHub's private vulnerability reporting
on this repository: open the Security tab and choose "Report a vulnerability".
That keeps the report private between you and the maintainers until a fix is
ready.

If you cannot use GitHub, email info@alpha-omega.dev with "hyrum security" in
the subject line.

We aim to acknowledge new reports within five working days and to agree a
disclosure timeline with you once the issue is confirmed; please do not open a
public issue for security problems.

## Reports written or found by AI tools

If you used an AI tool to find or write up the issue, say so in the report.

Before submitting, verify the finding yourself: confirm the affected code path
exists in this repository at the cited line, run the proof of concept, and
check that the behaviour matches what the tool claims. AI tools regularly
invent function names, file paths, and impact claims that do not hold up. A
report we cannot reproduce, that cites code that is not there, or that
proposes a fix using APIs that do not exist will be closed and may get the
account blocked from future reports.

Do not paste the tool's output as the report. Write what you actually
verified, in your own words, and keep it short.

## Supported versions

hyrum is not yet tagged; only the current `main` branch is supported. If you
find a problem in an older commit, check whether it still reproduces on `main`
before reporting.

## Severity

We rate confirmed issues as Low, Medium, High, or Critical and publish that
rating in the advisory. We do not set CVSS vectors: hyrum is an operator-run
CLI with no package-manager install base and no network-listening surface, so
most CVSS inputs do not map cleanly. If a downstream database assigns a CVSS
score to one of our advisories, that score is theirs.

## Scope

hyrum is a CLI that clones third-party repositories, fetches package-registry
metadata, and drives an LLM agent CLI over the result. Without `--container`
that agent runs directly on the operator's host. See
[`threatmodel.md`](threatmodel.md) for the trust boundaries, numbered threats,
mitigations, and known residuals. We are interested in reports where:

- a malicious dependency repository, target repository, or registry response
  can escape the workspace or container
- generated file output can write outside the `--out` directory
- `--verify` or `check` can be induced to execute code outside the scratch
  directory
- credentials passed via environment variable can leak to a third party

Issues that require the operator to point `--run` at hostile input on the host
without `--container` are lower priority but still welcome; the threat model
already recommends against that.

## Out of scope

These are not treated as security issues:

- Code execution inside the `--container` runner that stays inside the
  container. The agent runs with shell access by design; the container is the
  boundary.
- Gaps already listed as residuals in `threatmodel.md`. Reports that turn a
  documented residual into a working exploit are welcome and credited, but the
  severity reflects that the gap was already public.
- Prompt injection that only affects the content of the generated tests. The
  operator reviews and commits generated tests; they do not run automatically
  outside `--verify`.
- Resource exhaustion from a cloned repository (large clones, slow generation,
  oversized outline). Generation has a per-step cost cap and outline has a
  per-file size limit; a repository that hits those fails its own run.
- Anything that requires the attacker to already control the operator's host,
  the container runtime, or the environment variables hyrum is launched with.
- Issues in dependencies that hyrum does not reach. Run `govulncheck ./...`
  first; if it does not flag the path, neither will we.
