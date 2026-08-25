# Running generated tests on dependency-update PRs

A dependabot or renovate PR that bumps `ws` from 7.4.2 to 8.21.3 usually
passes the project's own tests, because those tests exercise the project's
code rather than the specific dependency behaviours it relies on. Running
`hyrum check` against the proposed version reports which of those behaviours
changed.

## One-time setup

Generate the suite once against the current baseline and commit it:

```
hyrum gen --dep ws --backend codex --run .
git add tests/hyrum
git commit -m "Add Hyrum's tests for ws"
```

The generated files are plain `node:test` (or pytest, or `go test`) files
under `tests/hyrum/<dep>/from_<target>/` with a `meta.json` recording the
baseline version and generation inputs. The target component uses the package
name from one root manifest when available. Nested targets add a stable suffix
from their Git-relative path, so repeated package names keep separate suites.
`gen --target-name <name>` can
set it explicitly.

If a backend exits non-zero after writing a fresh, usable output artifact,
Hyrum preserves that output and records `recovered_output: true` plus the
affected `recovered_steps` in `meta.json`. Raw backend output is not persisted.
The expected artifact is removed before every invocation, so output from an
earlier run cannot be recovered by mistake. Cancellation and provider-account
failures remain fatal even when an artifact exists.

## On each update PR

```yaml
# .github/workflows/hyrum.yml
on:
  pull_request:
    paths:
      - package.json
      - package-lock.json
jobs:
  check:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - run: go install github.com/alpha-omega-security/hyrum/cmd/hyrum@d83039d50e33f85051984fc92e069ec499faa1b0
      - run: hyrum check --dep ws@${{ steps.bump.outputs.version }}
```

`check` installs the candidate version in a scratch package environment and
runs `tests/hyrum/ws/`, exiting non-zero with the assertion diff when a pinned
behaviour has changed. The project's manifest, lockfile, and installed
dependencies remain unchanged:

```
→ npm add ws@8.21.3 in scratch
ws@8.21.3: FAIL
  ✖ delivers text message payloads as strings (1.2ms)
    AssertionError: Expected values to be strictly equal:
    + Buffer(17) [Uint8Array] [52, 116, 101, 115, 116, ...]
    - '4test pre-encoded'
```

The diff names the behaviour (`text message payloads as strings`), the source
in the project that depends on it (the test's comment cites
`lib/transports/websocket.js:14`), and what changed (Buffer where a string was
expected). That is the information needed to decide whether the bump requires
a code change or the test was over-specific.

## Regenerating after a major bump

When the project moves to ws 8 permanently, regenerate against the new
baseline so the suite pins ws-8 behaviour going forward:

```
hyrum gen --dep ws --backend codex --run .
```

The `meta.json` diff records the baseline change and the test-file diff is the
list of pinned behaviours that moved between the two versions.
