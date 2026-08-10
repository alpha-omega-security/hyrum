# Hyrum's Tests: applications beyond the headline use case

Written 2026-08-10 after re-running the PoC six months on. Baseline fixes in
PR #1 (andrew:fix-baseline-rot).

## Cutting transitive dependencies

The static + runtime analysis phases already produce a usage surface per
dependency (instrumentation/patterns.json, analysis/httpbin_flask_usage.md).
If httpbin uses 47 symbols from Flask but only 2 from six and 1 from
decorator, that's a vendoring/removal candidate list for free. Runtime call
counts strengthen it: a dep that appears in imports but registers zero calls
under the test suite is dead weight. Doesn't need the test-generation half at
all; falls out of phases 2-3.

## Consumer confidence on updates

The pitched use case. PoC supports it with the caveat that retrospective
detection was 60-70%. So "catch most breaks a day earlier than production"
rather than "high confidence". The more useful operational rule from
learnings.md: CVE patches within a major version almost never change behavior
for valid inputs, so fast-track patch bumps and reserve the full suite for
minor/major.

## Producer-side corpus

Bigger prize, underexplored. If Flask had a corpus of Hyrum's tests
contributed by httpbin, sentry, airflow, superset, etc., they'd have something
like Rust's crater or Go's corpus builder but cheaper to run (hermetic tests,
no need to build the whole downstream project). Current layout is
tests/hyrum/<downstream>/; flip to tests/hyrum/<upstream>/from_<downstream>/
and you have a registry an upstream can pull and run in CI. Security-update
angle: a maintainer shipping a CVE fix could run the corpus and attach
"0/1,847 downstream contract tests changed" to the advisory.

## Churn in aggregate codebase

Regenerate on a schedule and diff; the diff is a changelog of your dependency
surface. Growing surface = growing exposure. Less convinced this is worth
building; git log on the lockfile plus phase-2 usage analysis gets most of it.

## Churn in the dependency graph

ecosyste.ms territory more than Hyrum's tests territory. Release frequency,
commit velocity, maintainer count are all available without generating tests.
Where Hyrum's tests add something is the join:

    churn(dep) × surface_area(your usage) × historical_break_rate(dep)

gives a per-dependency risk score that neither dataset produces alone. The
patterns.json call counts are the surface_area term.

## Upstream test coverage vs consumer usage

Does the upstream's own test suite exercise the APIs downstreams actually
depend on? Take the 47 Flask symbols httpbin uses, run Flask's test suite
under coverage, report which of the 47 are hit. Gaps are APIs Flask ships but
doesn't test that real users depend on. Novel signal; data to compute it is
already collected in phase 2. Would make a good upstream PR: "here are 6
public functions your tests don't cover that N downstream projects call".

## Discovering breaking changes

PoC says yes for removals and type changes (9 caught), weakly for silent
behavioral changes (JSONIFY_PRETTYPRINT_REGULAR becoming a no-op wasn't caught
by an assertion, only noted in analysis). retrospective_analysis.md is the
honest accounting; misses are almost all "same API, different output".

## Logjams

PR mining (phase 4) already surfaces these: dependabot PRs open >30 days, or
requirements.txt lines with <N pins. Hyrum's tests add the why: run the suite
against pinned version and blocked version, and the diff is the concrete list
of breaks holding the upgrade back. Turns "werkzeug pinned to <3, nobody
remembers why" into "werkzeug pinned to <3 because parse_authorization_header
and WWWAuthenticate.set_digest are gone".

## Ranking by value ÷ additional build effort

1. Logjam explainer (near-zero extra work, phase 4 + existing tests)
2. Transitive dep cutting (near-zero, reuse phase 2-3 output)
3. Producer-side corpus (restructure test layout, most strategic value)
4. Upstream coverage gap report (needs a coverage-diff script, novel signal)
5. Risk scoring join with ecosyste.ms churn data (needs the join, both halves exist)
6. Consumer CI (already the headline, already works)

The producer corpus is the one that changes the economics. One downstream
generating tests for itself is a marginal CI improvement. A hundred
downstreams contributing to a shared corpus per upstream is a public good the
upstream can't build any other way.

## Second pass: usage surface as security data, not just breakage data

Everything above treats the phase 2-3 output (which symbols a downstream
calls, how often, with what arguments) as input to break detection. The same
data answers security questions.

### API surface complexity as a metric

Two numbers per dep: symbols exported vs symbols the downstream calls. httpbin
uses 47 of Flask's ~200 public names, 2 of six's ~60. The ratio is a
vendoring signal (already noted) but the absolute exported count is a proxy
for attack surface and maintenance burden. A dep exporting 800 symbols with 12
maintainer-hours/year is a different risk than one exporting 30. Cheap to
compute from the package's `__all__` or module dir() and worth adding as a
column in the usage-surface report.

### CVE reachability

Most CVE alerts are noise because the vulnerable function isn't in the
consumer's call graph. The usage surface is exactly the data needed to filter
them: pull the CVE's affected-function list (OSV records often have this, or
diff the fix commit), intersect with patterns.json, and the alert is either
"you call this, patch now" or "not reachable from your code, patch when
convenient". govulncheck does this for Go via static analysis; the runtime
instrumentation here would do it for Python/JS where static call graphs are
unreliable. This is probably the single highest-value security application.

### Footgun detection across dependents

Invert the direction. Generate usage surfaces for the top N dependents of one
upstream and look for misuse patterns that recur: yaml.load instead of
safe_load, requests.get with no timeout, subprocess with shell=True, jwt.decode
with verify=False. Each instance is a bug in one dependent; the aggregate is
evidence the upstream API is a footgun. Fix it once upstream (change the
default, add a warning, deprecate the unsafe form) and every dependent is
fixed on their next update. That's the leverage: one PR to requests adding a
default timeout does more than a thousand PRs to dependents adding timeout=30.

The instrumentation already captures call arguments (patterns.json records
kwarg names and value types), so "find every requests.get call across N repos
where timeout kwarg is absent" is a query over existing data, not new
collection.

### Prioritization

Start with a small N (5-10 upstreams, 20-50 dependents each), picked by
downloads × dependent count × past CVE count. If the footgun scan finds
nothing in the first batch the approach is probably too shallow to be worth
scaling; if it finds 2-3 real patterns per upstream it scales linearly with
compute.

Weaker ideas from this batch: "classify top N projects by complexity" on its
own is just a leaderboard; only useful as the prioritization input above.
"Identify security bugs in dependents" without the aggregate step is just
running semgrep on a lot of repos, which plenty of tools already do.

---

## Actions

- [ ] Restructure tests/hyrum/ from <downstream>/ to <upstream>/from_<downstream>/
      so a Flask maintainer can `pytest tests/hyrum/flask/` and get every
      contributor's contract tests in one run
- [ ] Write scripts/usage-surface.sh that reads instrumentation/patterns.json
      and emits a table of (dep, symbols used, call count, LoC if vendored) as
      the transitive-cut report
- [ ] Write scripts/logjam.sh <repo>: find pins with upper bounds + long-open
      dependabot PRs, run Hyrum's suite against pinned vs latest, emit the
      failing test list as the "why this is stuck" report
- [ ] Prototype the coverage-gap report: run Flask's own test suite under
      coverage.py, intersect with the 47 symbols from httpbin_flask_usage.md,
      list uncovered ones
- [ ] Sketch the ecosyste.ms join: pull release cadence + commit rate for each
      dep in patterns.json, multiply by call count, rank
- [ ] Add a second downstream for Flask (pick from sentry/airflow/superset) so
      the producer-corpus layout has >1 contributor and the merge story is real
- [ ] Decide whether behavioral assertions (output equality, not just
      hasattr/type) are worth the false-positive cost; current 60-70% detection
      ceiling is set by their absence
- [ ] Pitch the producer-corpus idea to one upstream (Flask or ws) and see if
      they'd run it in CI
- [ ] Add exported-symbol count per dep to the usage-surface report
      (used/exported ratio + absolute exported count as complexity proxy)
- [ ] CVE reachability prototype: pick one recent Python CVE with a known
      affected function, check whether httpbin's patterns.json reaches it,
      compare result to what pip-audit/safety would have said
- [ ] Footgun scan prototype: generate usage surfaces for 20 dependents of
      requests, count calls where timeout kwarg is absent; same for pyyaml
      load vs safe_load. One upstream, one pattern, prove the query works
      before scaling
- [ ] If footgun scan yields >0: write up the finding as an upstream issue
      ("N% of sampled dependents call X unsafely") rather than N dependent PRs

---

## Re-architecture: hyrum as a harness-driven tool (2026-08-10)

Goal: a single Go binary you point at a repo. It detects the toolchain,
enumerates direct dependencies, and for each one runs an LLM skill pipeline
that emits Hyrum's tests into a well-known layout. Same shape as scrutineer
but the output is a test suite instead of a findings report.

### Phase mapping: AGENT_SPEC.md → git-pkgs + harness

    Phase 1  Setup            clone.Ensure           clone target + each dep's source repo
                              brief                  toolchain, test command, layout
                              manifests / resolve    direct dep list with purls + versions
                              registries             dep repo URL, latest versions, release list
    Phase 2  Static analysis  outline.Pack           dep's public surface (signatures only) as LLM context
                              (gap: usage-index)     which dep symbols the target calls, with call-site snippets
    Phase 3  Runtime instr.   (gap)                  no cross-ecosystem equivalent; PoC tracers are Python-only
    Phase 4  History mining   forge                  PRs/issues mentioning the dep
                              git log --grep         commits mentioning the dep (via clone checkout)
                              changelog.Parse        upstream CHANGELOG breaking-change entries
                              vulns                  past CVEs on the dep (feeds reachability + risk score)
    Phase 5  Generation       harness.Job            run `hyrum-generate` skill with all of the above as context
    Phase 6  Validation       managers               install dep@baseline and dep@candidate
                              brief (test cmd)       run generated tests against each, diff pass/fail
                              downstream             producer-side: run the corpus in an upstream's CI

### What already exists and just needs wiring

- brief, manifests, resolve, registries, clone, forge, changelog, vulns,
  managers, outline: all cover their box above as-is.
- downstream: already implements discover-dependents → clone → baseline →
  replace → retest. For the producer corpus it needs one new mode: instead
  of running the dependent's own suite, run tests/hyrum/<upstream>/** if
  present. That's a --suite flag, not a new tool.
- harness: Job{Workspace, SrcDir, SkillName, Prompt, OutputFile} is exactly
  the interface needed. scrutineer's internal/worker/claude.go:toJob() is
  the pattern to copy.
- scrutineer skills/breaking-change: already reads a diff and names likely-
  broken dependents. Reusable as-is for the "will this security patch break
  anyone" question once a corpus exists.

### What's missing (candidate new git-pkgs libraries)

- usage-index: given (target checkout, dep purl), return the list of
  imported symbols and for each one the call sites with N lines of context.
  Cross-ecosystem via tree-sitter (outline already vendors gotreesitter).
  This replaces AGENT_SPEC's grep -r "^import" + grep -B2 -A10 with
  something that understands scoping. Output is the patterns.json shape
  minus runtime counts.
- Nothing else is strictly required for v1. Runtime instrumentation
  (phase 3) is the biggest known gap but the PoC's 60-70% detection came
  mostly from static + history mining; runtime added type-contract detail.
  Ship without it, add per-ecosystem tracers later.

### Skills to write (SKILL.md + schema.json each)

- hyrum-usage: reads ./src (target), ./dep-outline.md (from outline.Pack on
  the dep), ./usage-index.json; writes usage-surface.json with
  {symbol, call_sites[], arg_patterns[], notes}. Mid-tier model.
- hyrum-history: reads ./git-log.txt, ./prs.json (from forge),
  ./changelog.json; writes breaks.json listing past compatibility fixes
  with {commit, symbol, what_changed}. Mid-tier model.
- hyrum-generate: reads usage-surface.json + breaks.json + dep-outline.md;
  writes tests/hyrum/<dep>/from_<target>/test_*.{py,go,js} following the
  test template in AGENT_SPEC. High-tier model. This is where the current
  AGENT_SPEC.md content lands almost verbatim.
- hyrum-validate: reads test run output from managers-driven baseline and
  candidate installs; writes verdict.json classifying each failure as
  {real_break, brittle_test, env_issue}. Mid-tier model. Prunes brittle
  tests before commit.

### CLI shape

    hyrum gen <repo-url-or-path> [--dep <purl>]... [--out ./tests/hyrum]
      # clone, brief, resolve deps, run usage+history+generate per dep,
      # validate against installed and latest, write tests + report.json

    hyrum check [--dep <purl>@<new-version>]
      # run existing tests/hyrum/** against a candidate version
      # (the dependabot-PR use case)

    hyrum corpus <upstream-purl> --dependents N
      # downstream discover + hyrum gen on each dependent, aggregate into
      # one tests/hyrum/<upstream>/ tree (the producer use case)

    hyrum surface <repo> [--dep <purl>]
      # usage-index only, no LLM: emit the used/exported table for
      # transitive-cut and CVE-reachability reports

### Config

Follow scrutineer.yaml: backend (claude/codex/opencode), effort, model tier
per skill, container runtime, data dir. One hyrum.yaml with per-dep
overrides (skip, pin baseline version, extra test flags).

### Corpus layout (output contract)

    tests/hyrum/
      <upstream-name>/            # e.g. flask, ws, gin
        from_<downstream-name>/   # e.g. from_httpbin
          test_*.{py,go,js,rb}
          meta.json               # {generated_at, target_sha, dep_version,
                                  #  usage_surface_sha, skill_versions}
        README.md                 # aggregate: which downstreams contributed

meta.json is what lets `hyrum check` and `downstream` know what baseline
each test file was generated against, and what makes regeneration
idempotent (skip if target_sha and dep_version unchanged).

### Full-tree mode

"With enough tokens, full dependency tree analysis" = resolve gives the
whole tree, hyrum gen recurses. Cost control: usage-index on the target
tells you which transitive deps are actually reachable (most aren't), so
prune to reachable-only before spending LLM tokens. The surface command
above is the dry-run that shows the pruned list and estimated cost before
committing.

### First cut scope

One ecosystem end-to-end (npm, since managers/resolve/downstream support is
most mature there and the ws PoC tests already exist as a fixture). One
target (something small with 5-10 direct deps). Prove gen → validate →
check works, then add Python and Go which are mostly manager-command
substitutions.
