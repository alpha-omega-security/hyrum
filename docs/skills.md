# Model steps

`hyrum gen --run` invokes four model-driven steps in sequence, each a
[skill](../skills/) file (`SKILL.md` plus a JSON-schema output contract)
staged into the workspace and run headlessly by the configured backend. The
Go pipeline stages every non-model input first, so each step reads only
files, and the output of each step is a JSON file the next one reads.
Splitting the work this way keeps each prompt focused on one job, lets a
cheaper model handle the extraction and classification steps while a stronger
one writes tests, and makes each step's output inspectable in the workspace
after a run.

## Workspace layout

Before any model step runs, `stageContext` and `GatherHistory` write:

| file | source | consumed by |
|---|---|---|
| `target/` | symlink to the target repository | usage, generate |
| `dep/` | full clone of the dependency's source repo (best-effort) | history, generate |
| `usage.json` | scoped imports, refs, and configured activation strings over `target/` | usage, generate |
| `dep-outline.md` | `outline.Pack` of `dep/`, signature-only | usage, generate |
| `context.json` | purl, name, ecosystem, baseline version, repo URL, latest version, exported-symbol count | all |
| `git-log.txt` | target commits selected by dependency mentions in messages or manifest diff excerpts | history |
| `changelog.json` | `changelog.Between(baseline, latest)` from `dep/` (absent if none found) | history, validate |
| `vulns.json` | osv.dev advisories for the dep's purl | history |

## hyrum-usage

Reads `usage.json`, `target/`, `dep-outline.md`, `context.json`. Writes
`surface.json`.

Static extraction finds import entry points, configured activation strings,
and same-file member accesses, but the interesting behaviour is often on
values derived from those:
`const wss = new WsServer(opts)` followed elsewhere by
`this.wss.handleUpgrade(req, socket, head, cb)` where `cb`'s first argument
has `.readyState` read on it. This step follows each `usage.json` entry point
through the target's source and records the full call surface: receiver,
member, argument shapes, and what the target reads from the return value.
Each site retains its `production`, `test`, `example`, or `documentation`
scope. Generation stages production sites by default.
engine.io's one static entry point becomes fifteen traced calls; httpbin's
nine Flask imports become sixty-eight. Output shape:

```json
{"entry_points": [...], "calls": [
  {"receiver": "wss", "member": "handleUpgrade",
   "args": ["http.IncomingMessage", "net.Socket", "Buffer", "callback"],
   "returns": "reads .readyState on cb arg 0",
   "sites": [{"file": "lib/server.js", "line": 384, "scope": "production"}]}
], "notes": "..."}
```

Mid-tier model. If the backend fails or the file is absent, `hyrum-generate`
falls back to `usage.json`.

## hyrum-history

Reads `git-log.txt`, `changelog.json`, `vulns.json`, `context.json`, and for
each candidate commit `git -C target show <sha>`. Writes `breaks.json`.

The target's own git history is the strongest signal for which dependency
behaviours matter, because someone already fixed a break there once. This
step reads each commit that mentions the dependency, opens the diff to find
which symbol the fix touched, and cross-references the changelog for entries
between baseline and latest that say removed, renamed, changed default, or
changed return type. Each entry carries the evidence it was derived from so
`hyrum-generate` can cite it in the test's source comment. Output shape:

```json
{"breaks": [
  {"symbol": "message event payload", "change": "string → Buffer",
   "versions": ">=8.0.0",
   "evidence": {"kind": "commit", "ref": "64d5754",
                "quote": "fix: handle buffer message payloads from ws@8"}}
], "notes": "..."}
```

Mid-tier model. An empty `breaks` array is a valid result.

## hyrum-generate

Reads `surface.json` (or `usage.json`), `breaks.json`, `dep-outline.md`,
`target/`, `dep/`, `context.json`. Writes `tests.json`.

Writes one hermetic test per traced call in `surface.json` and one per entry
in `breaks.json`, importing only the dependency and standard library. The
skill's rules forbid inventing inputs the target does not use (a test for
`jsonify(float('nan'))` when httpbin never passes NaN is a false-positive
generator) and forbid existence-only assertions (`hasattr`,
`is not None`, `toBeDefined`), which is where 23% of the original proof of
concept's tests landed. Each test's docstring or leading comment cites the
`surface.json` file:line or `breaks.json` evidence it was derived from.
Output is `{files: [{path, content, source}], notes}`; the driver writes each
file under `tests/hyrum/<dep>/from_<target>/`.

High-tier model. This is the only required step; the pipeline continues past
a `hyrum-usage` or `hyrum-history` failure but returns an error here.

## hyrum-validate

Reads `tests.json`, `verify.json`, `surface.json`, `usage.json`,
`changelog.json`, `context.json`. Writes `verdict.json`.

Runs after `--verify` has installed the dependency at baseline and latest in
a scratch directory and run the generated tests against each; `verify.json`
holds per-version pass/fail counts, failing test names, and the raw
test-runner output so assertion diffs are available. For each test that
passes on baseline and fails on latest, this step classifies the failure:

- `real_break` (action `keep`): the dependency changed a behaviour the target
  reads, per `surface.json` or a changelog entry. The test is doing its job.
- `over_specific` (action `weaken` or `drop`): the assertion pins something
  incidental (whitespace, key ordering, error-message wording, an internal
  field the target never reads) that happened to change.
- `env` (action `fix`): the failure is unrelated to the version (missing
  test dependency, port conflict, timing).

Independently of `verify.json`, it also flags any test whose only assertion
is a shape check (`isinstance`, `typeof`, `is not None`) as `weak` (action
`strengthen`), which is the assertion-quality gate `hyrum-generate`'s rules
promise. Verdicts land in `meta.json` alongside the verify results.

Mid-tier model. Skipped when `--verify` was not passed or when no version's
tests were parsed at all.
