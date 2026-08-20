# hyrum

hyrum generates tests that pin how your code actually calls its dependencies,
so a version bump that changes something you rely on fails a test that names
the behaviour instead of surfacing three layers deep in your own suite.

[Hyrum's Law](https://www.hyrumslaw.com): with enough users, every observable
behaviour of your system will be depended on by somebody, whether you
documented it or not. A library's own tests cover the contract its maintainers
intend; hyrum generates the tests for the contract you accidentally have.

The approach and the httpbin/Flask baseline below come from Michael Winser's
[hyrums-tests](https://github.com/michaelwinser/hyrums-tests) proof of
concept, which built the six-phase methodology (setup, static analysis,
runtime tracing, history mining, generation, validation) by hand for one
project. This tool turns those phases into a pipeline for repositories across
package-manager ecosystems, built on the
[git-pkgs](https://github.com/git-pkgs) libraries so per-ecosystem work stays
upstream.

## Example

[engine.io](https://github.com/socketio/engine.io) depends on
[`ws`](https://www.npmjs.com/package/ws) for its WebSocket transport. `hyrum
gen --dep ws --run --verify ./engine.io` extracts the one static entry point
(`const DEFAULT_WS_ENGINE = require("ws").Server`), traces it through the
instance to
fifteen method calls (`handleUpgrade`, `shouldHandle`, `close`, the
per-message-deflate options, ...), mines engine.io's git history and the ws
changelog for past compatibility fixes, and writes seven hermetic tests using
`node:test` and an in-memory Duplex stream. The recorded generation run cost
$1.27.

Against ws 7.4.2, engine.io's pinned version, all seven pass. Against ws
8.21.3 one fails: `delivers text message payloads as strings`, because ws 8
changed the message-event payload from `string` to `Buffer`. The test's source
comment cites engine.io commit `64d5754`, where a maintainer applied exactly
that fix when the upgrade landed.

On the original proof of concept's target,
[httpbin](https://github.com/postmanlabs/httpbin), the static step alone
reproduces the hand analysis: `hyrum surface --dep flask ./httpbin` (no model
calls) finds all nine named [Flask](https://pypi.org/project/Flask/) imports
and fourteen `request` attributes across 63 call sites. The full pipeline on
the same target produces 73 tests for $2.47, none of
which use `hasattr`- or `is None`-only assertions (23% of the PoC's 299 do),
and one of which pins the exact `Set-Cookie` header from
`response.delete_cookie()`. That test fails on
[Werkzeug](https://pypi.org/project/Werkzeug/) 2.3+ because the expiry-date
format changed from `01-Jan-1970` to `01 Jan 1970`; the PoC's
`delete_cookie` test asserts a substring and passes on both.

## Use cases

`gen` writes to `tests/hyrum/<dependency>/from_<target>/` by default. `corpus`
writes the same layout under its required `--out` directory, so the same suite
can serve both the consumer and the dependency's maintainer. Worked examples
with real command output are in [docs/](docs/).

**Consumer CI** ([docs/consumer-ci.md](docs/consumer-ci.md)). Run
`tests/hyrum/<dep>/` on the [dependabot](https://docs.github.com/en/code-security/dependabot)
or [renovate](https://docs.renovatebot.com) PR that bumps `<dep>`; a
failure names the specific behaviour the new version changed, which is
usually clearer than the same break surfacing through an unrelated
integration test.

**Producer corpus** ([docs/producer-corpus.md](docs/producer-corpus.md)).
`hyrum corpus --upstream pkg:npm/ws --discover 20 --out ./ws-corpus --run
--container default` clones the top dependents by popularity, generates each
one's `from_<dependent>/` suite, and aggregates them so a `ws` maintainer can
run every consumer's contract tests before tagging a release. Rust's
[crater](https://github.com/rust-lang/crater) and Google's TAP do this at
whole-ecosystem scale by building and running every dependent; a small
per-consumer suite aims to retain the relevant compatibility signal without
building the dependent in full.

**Pinned-dependency diagnosis** ([docs/logjam.md](docs/logjam.md)). For npm,
when a dependency is held back by an upper-bound pin and the reason has been
lost, `hyrum check --dep X@<blocked-version>` runs the generated suite against
the blocked version and prints which assertions fail. Version selection for
PyPI and Go is still incomplete, as described below.

**CVE reachability and vendoring**
([docs/reachability.md](docs/reachability.md)). `hyrum surface --json` gives
used-symbol and call-site counts for each dependency; add `--dep X` to get the
symbol and source-site list for one dependency. Neither form makes model
calls. Intersecting used symbols with a CVE's affected functions can narrow an
advisory, and a dependency where the target uses two of two hundred exported
symbols may be a vendoring candidate.

## Rationale

Testing your dependencies used to be bad advice for a cost reason: writing and
maintaining hundreds of tests against someone else's API took more
engineer-hours than the breakage it prevented. Generating and regenerating
them turns that into a token spend, and coupling to a dependency's behaviour
is fine when regenerating for a new baseline is cheap. That you cannot fix the
library yourself is beside the point (the test tells you before production
does), and the library having its own tests is exactly the Hyrum's Law gap.

## Install

```
go install github.com/alpha-omega-security/hyrum/cmd/hyrum@latest
```

Requires [Go 1.26+](https://go.dev/dl/) and `git` on PATH. `hyrum surface`
needs nothing else. `hyrum check` and `gen --verify` also use the target's
package manager and test runtime. The implemented runners require Node for
npm, Python with pytest for PyPI, or Go for Go modules.

## Backend setup

`gen --run` and `corpus --run` drive one of the CLI agent tools (`claude`,
`codex`, `copilot`, `opencode`) headlessly. The
[alpha-omega-security/harness](https://github.com/alpha-omega-security/harness)
library normalises their argv, streaming output, and skill staging so the
pipeline is agnostic to which one is behind `--backend`; the default is
`claude`.

For the claude backend, either a Claude Code subscription token from the
[CLI](https://docs.anthropic.com/en/docs/claude-code):

```
claude setup-token
export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...
```

or an API key from [console.anthropic.com](https://console.anthropic.com):

```
export ANTHROPIC_API_KEY=sk-ant-api03-...
```

For codex, `export CODEX_API_KEY=sk-...`. Copilot accepts
`COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, or `GITHUB_TOKEN`. For opencode, use
`OPENAI_API_KEY` or `ANTHROPIC_API_KEY` depending on its configured provider.

## Configuration

`hyrum gen` and `hyrum surface` automatically load `hyrum.yaml` from the
analyzed target repository's root when that file exists. They do not search
parent directories. Use `--config <path>` to load a different file; a missing,
unreadable, malformed, or invalid explicit file is an error, while an absent
automatic file is not. Configuration is strict: unknown keys and incorrect
value types are rejected.

The supported settings are `backend`, `out`, `work`, per-skill `models`, and
per-dependency `baseline`, `skip`, and `activations` values under `deps`.
Explicit command-line flags take precedence over config, and config takes
precedence over built-in defaults. Relative `out` paths are rooted at the
target repository. For safety, `out` in an automatically discovered target
configuration must remain inside that target; an explicitly supplied
`--config` may select an external output path.

An automatically discovered config cannot select `work`: that value is ignored
unless the operator supplies the config with `--config`. `--work` is always
honored. A relative `work` value from an explicit config is rooted at the
directory containing that file; absolute paths and `~/...` are accepted.

Dependency overrides may be keyed by manifest name or full purl; a purl entry
wins for fields also set by a name entry. `skip: true` removes a dependency
from default generation, but an explicit `--dep` includes it. `baseline`
changes the version staged, verified, and recorded by Hyrum without modifying
the target's manifest or lockfile.

`activations` lists exact quoted string literals that select a dependency
without importing it directly. Driver names, plugin aliases, dynamic import
names, and entry-point identifiers can be recorded this way. Each match appears
in the static surface with kind `activation`, its source line, and its path
scope. Comment-only lines are ignored.

Model values are portable `mid`, `high`, or `max` tiers. Each selected backend
maps the tier to a model through the harness API, so model selection applies
to individual skills without backend-specific configuration. See
[`hyrum.sample.yaml`](hyrum.sample.yaml) for the complete schema.

## Host and container modes

By default the backend runs on the host, so its CLI (`claude`, `codex`, ...)
must be on PATH and file-based logins under `~/.claude` or `~/.codex` work.
`--container default` runs it inside
`ghcr.io/alpha-omega-security/scrutineer-runner` instead: the image bundles
all four backend CLIs plus Node, Python, and Go toolchains. The container has a
fresh HOME, the target repository mounted read-only, and dropped capabilities,
which is the recommended mode for `corpus --discover` or any target you did
not author; see [SECURITY.md](SECURITY.md) and
[threatmodel.md](threatmodel.md). Because HOME is fresh, authentication in
container mode must come from an environment variable rather than a login
file, and the image is pulled on first use.

Container mode applies only to the model backend. `gen --verify` and `check`
still run package-manager installs and generated tests on the host, so their
local runtimes are required even when `--container` is set.

## Usage

```
hyrum surface <path>                  per-dep usage summary; no model calls
hyrum surface --dep X <path>          symbol-level detail for one dep
hyrum surface --dep X --symbol X.y ... inspect selected exact symbols
hyrum surface --scope test <path>     restrict the summary to test usage
hyrum gen --dep X --run <path>        generate tests for X into tests/hyrum/
hyrum gen --dep X --batch-size 40 ... cap each model batch at 40 symbols
hyrum gen --dep X --batch-sites 500 ... cap each model batch at 500 sites
hyrum gen --dep X --run --verify ...  also run tests at baseline and latest
hyrum check --dep X@<ver> <path>      run existing tests/hyrum/X against X@ver
hyrum corpus --upstream <purl> \
  --discover N --out <dir> --run      generate from_<dependent>/ for top N
```

Static indexing and generation cover all ecosystems listed below. Test
execution is narrower: npm works end to end, PyPI and Go have known scratch
setup or version-selection gaps, and the other four ecosystems have no runner
yet.

Static sites are classified as `production`, `test`, `example`, or
`documentation` from their relative paths. `surface` includes every scope and
reports separate production, test, and other site counts. `gen` and `corpus`
stage production sites by default. Repeat `--scope` to select more than one
scope. `surface` and `gen` also accept repeatable `--include` and `--exclude`
relative path prefixes; exclusions take precedence. Repeatable `--symbol`
values select exact, case-sensitive static symbol names and require one
explicit `--dep`.

`gen --batch-size N` caps symbol entries per model batch, while
`--batch-sites N` caps static sites. The selected symbols and their sites are
sorted before partitioning. A symbol with more sites than the limit is split
across batches. History mining runs once, and its contracts are included in
the first batch. When a run needs several batches, generated files are placed
under `batch-001/`, `batch-002/`, and so on within the dependency suite.
Verification and validation run once over the merged files and traced surface.
`meta.json` records the symbols, site count, backend session, cost, notes, and
recovered steps for each batch. A staging run without `--run` writes each
batch's `usage.json` under the workspace's `batches/` directory for inspection.

`gen` stages a workspace with the target's static usage of the dependency
(`usage.json`), a signature-only outline of the dependency's source
(`dep-outline.md`), the target's git commits mentioning the dependency, its
OSV advisories, and its parsed changelog. Without batching, three model steps
run in sequence: `hyrum-usage` follows the static entry points through
instances and options bags to record the actual call surface;
`hyrum-history` filters the history inputs down to a list of past compatibility
fixes with evidence for each; `hyrum-generate` writes tests that mirror the
observed calls and cite the source line or commit each was derived from. With
`--verify`, supported
tests are run in a scratch directory against the target's baseline version and
the dependency's latest release. A fourth `hyrum-validate` step then classifies
latest-version failures as real behaviour changes, over-specific assertions,
or environment problems, and flags tests whose only assertion is a shape
check. Per-version results and per-test verdicts land in `meta.json`. See
[docs/skills.md](docs/skills.md) for what each step reads and writes.

## Ecosystems

Repository and package detection, registry metadata, package-manager
operations, source parsing, changelog parsing, advisory lookup, and cloning
come from the [git-pkgs](https://github.com/git-pkgs) libraries. `corpus`
discovers dependents through [ecosyste.ms](https://ecosyste.ms).

Static indexing records imports and direct member references. The model-backed
usage step can then follow values through the target to find calls on returned
instances or callbacks.

| purl type | languages | static extraction | test execution |
|---|---|---|---|
| npm | JavaScript, TypeScript | `require()`, ESM `import`, chained `.member` | `node --test` |
| pypi | Python | `import`, `from ... import`, attribute access | pytest; scratch setup and version pinning incomplete |
| golang | Go | `import "path"`, selectors, qualified types | `go test`; version pinning and result counts incomplete |
| gem | Ruby | `require`, `Const::` and `Const.method` refs | not implemented |
| cargo | Rust | `use crate::`, `crate::path` refs | not implemented |
| composer | PHP | `use Vendor\...`, `Vendor\X` refs (case-folded) | not implemented |
| hex | Elixir | `alias`/`import`/`use`, `Module.fun` refs | not implemented |

## Security

`gen --run` and `corpus --run` feed third-party source code, changelogs,
registry metadata, and OSV advisory text to an LLM backend that has a shell
tool enabled. Any of that content can carry prompt-injection text, and on the
host the backend runs as you. `--container` bounds that to an ephemeral
container with a fresh HOME, dropped capabilities, and the target mounted
read-only, and is the recommended mode for `corpus --discover` or for `gen`
against any checkout you did not author. See [SECURITY.md](SECURITY.md) for
the reporting policy and [threatmodel.md](threatmodel.md) for the trust
boundaries, numbered threats, and known residuals.

Generated tests are model output. The generation skill instructs the backend
to import only the dependency and standard library and to avoid network and
external filesystem access, but the CLI does not enforce those rules. Review
tests before running them. `gen --verify` and `check` run tests and package
install hooks as your user. `check --dep X@<version>` may also change the
target's manifest or lockfile.

## License

MIT. See [LICENSE](LICENSE).
