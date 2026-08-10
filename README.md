# hyrum

hyrum turns the [hyrums-tests](https://github.com/michaelwinser/hyrums-tests)
proof of concept into a pipeline you can point at any repository. Given a
target and a dependency, it extracts how the target's code calls that
dependency, collects the target's git commits and the dependency's changelog
and advisories for past compatibility fixes, and emits hermetic tests that pin
the observed behaviour so the suite passes on the dependency version the
target was built against and fails when a later version changes something the
target relies on.

Running the pipeline on engine.io against `ws` produces seven tests using
`node:test` and an in-memory Duplex stream (no ports, no timing), for $1.27
across the three model-driven steps. Against ws 7.4.2, engine.io's baseline,
all seven pass; against ws 8.21.3 one fails, `delivers text message payloads
as strings`, because ws 8 changed the message-event payload from `string` to
`Buffer`. The failing test's source comment cites engine.io commit `64d5754`,
where that fix was applied by a maintainer.

The static extraction step alone, with no model calls, reproduces the original
httpbin/Flask hand analysis: `hyrum surface --dep Flask ./httpbin` finds all
nine named imports and all ten `request` properties the proof of concept
documented, plus three the manual pass missed. The full pipeline on the same
target produces 73 tests for $2.47, none of which use `hasattr`- or
`is None`-only assertions (23% of the proof of concept's 299 do), and one of
which pins the exact `Set-Cookie` header from `response.delete_cookie()`. That
test fails on Werkzeug 2.3+ because the expiry-date format changed from
`01-Jan-1970` to `01 Jan 1970`; the proof of concept's `delete_cookie` test
asserts only a substring and passes on both.

## Rationale

Testing your dependencies used to be bad advice for a cost reason: writing and
maintaining hundreds of tests against someone else's API took more
engineer-hours than the breakage it prevented. LLM generation and regeneration
turn that into a token spend, and coupling to a dependency's behaviour is
acceptable when regenerating the suite for a new baseline is cheap. The other
usual arguments carry less weight here: the point of these tests is to find
out you are broken before production does, so being unable to fix the library
yourself is beside it, and Hyrum's Law is precisely that a library's own suite
covers the contract its maintainers intend rather than every observable
behaviour a caller ends up relying on.

## Install

```
go install github.com/alpha-omega-security/hyrum/cmd/hyrum@latest
```

Go 1.26 or later. `git` must be on PATH. For `gen --run` and `corpus --run`
you also need one of the [harness](https://github.com/alpha-omega-security/harness)
backends installed and authenticated (`claude`, `codex`, `copilot`, or
`opencode`); pass it with `--backend`.

## Usage

```
hyrum surface <path>            per-dep usage summary; no model calls
hyrum surface --dep X <path>    symbol-level detail for one dep
hyrum gen --dep X --run <path>  generate tests for X into tests/hyrum/
```

`gen` stages a workspace containing the target's static usage of the
dependency (`usage.json`), a signature-only outline of the dependency's source
(`dep-outline.md`), the target's git commits that mention the dependency, its
OSV advisories, and its parsed changelog when one exists. The `hyrum-usage`
skill then follows the import entry points through instances and options bags
to record the actual method calls in `surface.json`; `hyrum-history` filters
the commit log and changelog down to `breaks.json`, a list of past
compatibility fixes with the evidence for each; `hyrum-generate` takes both
files plus the outline and writes tests that mirror the observed calls and
cite the source line or commit each was derived from.

## Output

The output layout is `tests/hyrum/<dependency>/from_<target>/` so that a
maintainer of the dependency can collect the `from_*` directories contributed
by many targets and run them as one suite before a release. That gives an
open-source project something like Rust's crater run without needing to build
every downstream: hermetic per-consumer contract tests that finish in seconds.
The same suite in a consumer's CI catches breakage on the next dependabot PR.

The per-dependency used-symbol count from `surface` ranks vendoring
candidates, and intersecting the used-symbol set with a CVE's
affected-function list turns a noisy advisory into a reachable/unreachable
call. For a dependency held back by an upper-bound pin, `hyrum check --dep
X@<blocked>` prints the specific failing behaviours instead of just a red CI.

## Ecosystems

Toolchain detection ([brief](https://github.com/git-pkgs/brief)), manifest and
lockfile parsing ([manifests](https://github.com/git-pkgs/manifests), 44
formats), registry metadata ([registries](https://github.com/git-pkgs/registries),
25 registries), package-manager operations
([managers](https://github.com/git-pkgs/managers), 36 CLIs), source outlining
([outline](https://github.com/git-pkgs/outline), 35 languages via
tree-sitter), changelog parsing, OSV lookup, and cloning all come from
[git-pkgs](https://github.com/git-pkgs). Dependent discovery for `corpus`
comes from [ecosyste.ms](https://ecosyste.ms) via
[enrichment](https://github.com/git-pkgs/enrichment). LLM invocation goes
through [harness](https://github.com/alpha-omega-security/harness), so the
backend is `claude`, `codex`, `copilot`, or `opencode` behind a `--backend`
flag. The only per-ecosystem code in this repository is a usage indexer that
maps a dependency name to the target files referencing it:

| Ecosystem | Languages | Entry-point matching |
|---|---|---|
| npm | JavaScript, TypeScript | `require()`, `import`, chained `.member` |
| pypi | Python | `import`, `from ... import`, attribute access |
| golang | Go | `import "module/path"` |
| gem | Ruby | `require 'gem'`, `GemName::` constant refs |
| cargo | Rust | `use crate::` |
| composer | PHP | `use Vendor\...` (PSR-4 heuristic) |
| hex | Elixir | `alias`/`import`/`use Module` |

Adding another ecosystem is a `Register` call in
`internal/hyrum/usage/generic.go` with an extension set and a match function
against the lines outline preserves. The match functions are heuristic where a
package name differs from the importable name (composer PSR-4 namespaces,
Rails autoload, PyYAML installing as `yaml`); the exact mapping is the domain
of [git-pkgs/provides](https://github.com/git-pkgs/provides), and outline's
structured `Imports()` API
([outline#27](https://github.com/git-pkgs/outline/issues/27)) removes the text
scan.

## License

MIT. See [LICENSE](LICENSE).
