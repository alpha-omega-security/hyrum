# hyrum

hyrum turns the [hyrums-tests](https://github.com/michaelwinser/hyrums-tests)
proof of concept into a pipeline you can point at any repository. It reads how
a target calls each of its dependencies, mines the target's own history for
past compatibility fixes, and writes hermetic tests that pin those observed
behaviours. The tests pass on the dependency version the target was built
against and fail when a later version changes something the target depends on.

Running the full pipeline on engine.io against `ws` produces seven tests using
`node:test` and an in-memory Duplex stream (no ports, no timing). All seven
pass on ws 7.4.2, engine.io's baseline. Six pass on ws 8.21.3; the seventh,
`delivers text message payloads as strings`, fails because ws 8 changed the
message-event payload from `string` to `Buffer`. The test's source comment
cites engine.io commit `64d5754`, where a human made exactly that fix. Total
cost across the three model-driven steps was $1.27.

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
maintaining hundreds of tests against someone else's API was more
engineer-hours than the breakage it prevented. LLM generation and regeneration
turn that into a token spend, and coupling to a dependency's behaviour is fine
when uncoupling is cheap. Of the remaining objections, "you can't fix the
library" misreads the goal, which is finding out you're broken before
production does. "The library has its own tests" is the Hyrum's Law gap: a
library's tests cover the contract its maintainers intend, which routinely
omits behaviour a caller has come to rely on.

## Usage

```
hyrum surface <path>            per-dep usage summary; no model calls
hyrum surface --dep X <path>    symbol-level detail for one dep
hyrum gen --dep X --run <path>  generate tests for X into tests/hyrum/
```

`gen` stages a workspace with the target's usage of the dependency
(`usage.json`), a signature-only outline of the dependency's source
(`dep-outline.md`), the target's git commits mentioning the dependency, its
OSV advisories, and its parsed changelog when one exists. Three skills run in
sequence over that workspace: `hyrum-usage` traces from import entry points
through instances and options bags to the actual method calls; `hyrum-history`
reads the git log and changelog for past compatibility fixes; `hyrum-generate`
turns both into tests that mirror the observed calls and cite their source.

## Output

The output layout is `tests/hyrum/<dependency>/from_<target>/` so that a
maintainer of the dependency can collect the `from_*` directories contributed
by many targets and run them as one suite before a release. That gives an
open-source project something like Rust's crater run without needing to build
every downstream: hermetic per-consumer contract tests that finish in seconds.
The same suite in a consumer's CI catches breakage on the next dependabot PR.

The `surface` data has uses on its own: the per-dependency used-symbol count
ranks vendoring candidates, and intersecting the used-symbol set with a CVE's
affected-function list turns a noisy advisory into a reachable/unreachable
call. For a dependency held back by an upper-bound pin, `hyrum check --dep
X@<blocked>` prints the specific failing behaviours instead of just a red CI.

## Dependencies

Toolchain detection, manifest parsing, registry lookup, cloning, source
outlining, changelog parsing, and OSV lookup come from the
[git-pkgs](https://github.com/git-pkgs) libraries. LLM invocation goes through
[harness](https://github.com/alpha-omega-security/harness), so the backend is
`claude`, `codex`, `copilot`, or `opencode` behind a `--backend` flag.

## Build

```
go build ./cmd/hyrum
```

Go 1.26 or later.

## License

MIT. See [LICENSE](LICENSE).
