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
project. This
tool turns those phases into a pipeline that runs against any repository in
seven package-manager ecosystems, built on the
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
`node:test` and an in-memory Duplex stream, for $1.27 across the three model
steps.

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

Output lands in `tests/hyrum/<dependency>/from_<target>/` so the same suite
serves both the consumer and the dependency's maintainer. Worked examples with
real command output are in [docs/](docs/).

**Consumer CI** ([docs/consumer-ci.md](docs/consumer-ci.md)). Run
`tests/hyrum/<dep>/` on the [dependabot](https://docs.github.com/en/code-security/dependabot)
or [renovate](https://docs.renovatebot.com) PR that bumps `<dep>`; a
failure names the specific behaviour the new version changed, which is
usually clearer than the same break surfacing through an unrelated
integration test.

**Producer corpus** ([docs/producer-corpus.md](docs/producer-corpus.md)).
`hyrum corpus --upstream pkg:npm/ws --discover 20` clones the top dependents
by download count, generates each one's `from_<dependent>/` suite, and
aggregates them so a `ws` maintainer can run every consumer's contract tests
before tagging a release. Rust's
[crater](https://github.com/rust-lang/crater) and Google's TAP do this at
whole-ecosystem scale by building and running every dependent; a hermetic
per-consumer suite gets most of the signal in seconds per dependent.

**Pinned-dependency diagnosis** ([docs/logjam.md](docs/logjam.md)). When a
dependency is held back by an upper-bound pin and the reason has been lost,
`hyrum check --dep X@<blocked-version>` runs the generated suite against the
blocked version and prints which assertions fail, giving the pin a concrete
justification again.

**CVE reachability and vendoring**
([docs/reachability.md](docs/reachability.md)). `hyrum surface --json` alone,
no model calls, gives a per-dependency used-symbol count and site list.
Intersecting used symbols with a CVE's affected functions turns a noisy
advisory into a reachable/unreachable call, and a dependency where the target
uses two of two hundred exported symbols is a vendoring candidate.

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

Requires [Go 1.26+](https://go.dev/dl/) and `git` on PATH. `hyrum surface` and
`hyrum check` need nothing else.

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

For codex, `export CODEX_API_KEY=sk-...`; for opencode, `OPENAI_API_KEY` or
`ANTHROPIC_API_KEY` depending on which provider it is configured with.

## Host and container modes

By default the backend runs on the host, so its CLI (`claude`, `codex`, ...)
must be on PATH and file-based logins under `~/.claude` or `~/.codex` work.
`--container default` runs it inside
`ghcr.io/alpha-omega-security/scrutineer-runner` instead: the image bundles
all four backend CLIs plus node, python, and go toolchains, so nothing needs
installing locally beyond Docker or Podman. The container has a fresh HOME,
the target repository mounted read-only, and dropped capabilities, which is
the recommended mode for `corpus --discover` or any target you did not author;
see [SECURITY.md](SECURITY.md) and [threatmodel.md](threatmodel.md). Because
HOME is fresh, authentication in
container mode must come from an environment variable rather than a login
file, and the image is pulled on first use.

## Usage

```
hyrum surface <path>                  per-dep usage summary; no model calls
hyrum surface --dep X <path>          symbol-level detail for one dep
hyrum gen --dep X --run <path>        generate tests for X into tests/hyrum/
hyrum gen --dep X --run --verify ...  also run tests at baseline and latest
hyrum check --dep X@<ver> <path>      run existing tests/hyrum/X against X@ver
hyrum corpus --upstream <purl> \
  --discover N --out <dir> --run      generate from_<dependent>/ for top N
```

`gen` stages a workspace with the target's static usage of the dependency
(`usage.json`), a signature-only outline of the dependency's source
(`dep-outline.md`), the target's git commits mentioning the dependency, its
OSV advisories, and its parsed changelog. Three model steps then run in
sequence: `hyrum-usage` follows the static entry points through instances and
options bags to record the actual call surface; `hyrum-history` filters the
history inputs down to a list of past compatibility fixes with evidence for
each; `hyrum-generate` writes tests that mirror the observed calls and cite
the source line or commit each was derived from. With `--verify`, the
generated tests are run in a scratch directory against the target's baseline
version and the dependency's latest release, and a fourth `hyrum-validate`
step classifies each latest-version failure as a real behaviour change,
an over-specific assertion, or an environment problem, plus flags any test
whose only assertion is a shape check. Per-version results and per-test
verdicts land in `meta.json`. See [docs/skills.md](docs/skills.md) for what
each step reads and writes.

## Ecosystems

Toolchain detection ([brief](https://github.com/git-pkgs/brief)), manifest and
lockfile parsing ([manifests](https://github.com/git-pkgs/manifests), 44
formats), registry metadata
([registries](https://github.com/git-pkgs/registries), 25 registries),
package-manager operations
([managers](https://github.com/git-pkgs/managers), 36 CLIs), source outlining
and structured import extraction
([outline](https://github.com/git-pkgs/outline), tree-sitter, 19 languages
for imports), package-identity to source-name mapping
([provides](https://github.com/git-pkgs/provides)), changelog parsing, OSV
lookup, and cloning all come from [git-pkgs](https://github.com/git-pkgs).
Dependent discovery for `corpus` comes from
[ecosyste.ms](https://ecosyste.ms) via
[dependents](https://github.com/git-pkgs/dependents).

`outline.Imports` returns each import statement's module path, kind, bound
names, and local aliases; `outline.Refs` returns direct member accesses on
those aliases. Matching an import's module path back to a dependency PURL is
`provides`: a curated Python catalog covers distributions whose module name is
unrelated to the registry name ([PyYAML](https://pypi.org/project/PyYAML/)
installs `yaml`, [Pillow](https://pypi.org/project/Pillow/) installs `PIL`),
and a naming-convention resolver handles the common case for every
ecosystem below. What remains here per ecosystem is a `specs` entry in
[`internal/hyrum/usage/index.go`](internal/hyrum/usage/index.go) (file
extensions, whether the language allows referencing a dependency's top-level
name without an import line) and a test-runner command for `check`/`--verify`.

| purl type | languages | outline extracts |
|---|---|---|
| npm | JavaScript, TypeScript | `require()`, ESM `import`, chained `.member` |
| pypi | Python | `import`, `from ... import`, attribute access |
| golang | Go | `import "path"`, selectors, qualified types |
| gem | Ruby | `require`, `Const::` and `Const.method` refs |
| cargo | Rust | `use crate::`, `crate::path` refs |
| composer | PHP | `use Vendor\...`, `Vendor\X` refs (case-folded) |
| hex | Elixir | `alias`/`import`/`use`, `Module.fun` refs |

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

## License

MIT. See [LICENSE](LICENSE).
