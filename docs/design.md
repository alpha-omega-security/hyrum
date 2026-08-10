# hyrum: re-architecture plan

2026-08-10. Turns the hyrums-tests PoC into a reusable Go tool built on
alpha-omega-security/harness and the git-pkgs libraries. Reference
implementations: scrutineer for the harness/skill/container pattern,
git-pkgs/downstream for the dependent-discovery loop, dotnet/skills
code-testing-generator for the assertion-quality gate.

## Why now

Classic testing advice said don't test your dependencies. That rested on: you
can't fix the library; it has its own tests; coupling your CI to it makes
upgrades painful; and writing hundreds of tests against someone else's API
isn't worth the engineer-hours. LLMs defeat the last one directly (generation
and regeneration are token spends, not engineer-hours) and the third
indirectly (coupling is fine if uncoupling is cheap — regenerate against the
new baseline instead of hand-editing 150 tests). The first was always a
misread of the goal, and the second is the Hyrum's Law gap: the library's
tests cover what its maintainers think the contract is, not what you depend
on. Same flip on the producer side: corpus testing that used to need
Google-scale infra becomes `pytest tests/hyrum/flask/` on hermetic
per-consumer suites.

## Goal

    hyrum gen <repo>

points at an application or library, detects its toolchain, enumerates direct
dependencies, and for each one produces a hermetic test suite that captures how
the target calls that dependency. Tests land in
`tests/hyrum/<upstream>/from_<target>/` with a `meta.json` recording the
generation inputs. The same suite serves the consumer (run on dependabot PRs),
the producer (aggregate across dependents and run in upstream CI), and the
analytical use cases (usage surface → transitive-cut, CVE reachability,
footgun aggregation).

## Building blocks (all exist today)

    clone         shallow-clone target and each dep's source repo
    brief         toolchain, test command, source layout
    manifests     parse manifest/lockfiles → direct deps with version constraints
    resolve       parse `npm ls`/`go mod graph`/etc → full tree with purls
    registries    dep → repo URL, release list, latest version
    outline       reduce dep source to signatures-only markdown for LLM context
    forge         PRs/issues mentioning the dep (history mining)
    changelog     parse upstream CHANGELOG into structured entries
    vulns         past CVEs on the dep
    managers      install dep@version, replace with local path (validation)
    downstream    discover dependents via ecosyste.ms, clone, baseline/replace/retest
    harness       drive claude/codex/copilot/opencode headlessly with a Job

## New code

### git-pkgs/usage (new library)

Given (checkout path, dep purl), return which of the dep's exported symbols
the checkout imports, and for each symbol the call sites with N lines of
context. Cross-ecosystem via tree-sitter (outline already vendors
gotreesitter so the grammars are available). Output shape:

    {
      "dep": "pkg:pypi/flask@2.3.3",
      "exported_count": 187,
      "used": [
        {"symbol": "flask.jsonify", "sites": [
          {"file": "core.py", "line": 412, "context": "..."},
          ...
        ], "arg_patterns": ["dict", "**kwargs"]},
        ...
      ]
    }

This replaces AGENT_SPEC's `grep -r "^import"` + `grep -B2 -A10` with
scope-aware extraction. It is also the LLM-free `hyrum surface` command that
feeds transitive-cut and CVE-reachability without spending tokens.

### cmd/hyrum (new binary)

Go, structured like scrutineer minus the web UI and DB. Subcommands:

    hyrum gen <repo> [--dep <purl>]... [--out ./tests/hyrum]
    hyrum check [--dep <purl>@<version>]
    hyrum corpus <upstream-purl> [--dependents N]
    hyrum surface <repo> [--dep <purl>] [--json]

Config in `hyrum.yaml`: backend, effort, model tier per skill, container
runtime, per-dep overrides (skip, baseline pin, extra test flags).

### Skills (SKILL.md + schema.json each)

    hyrum-usage      ./src + ./dep-outline.md + ./usage.json
                     → usage-surface.json {symbol, call_sites, arg_patterns, notes}
                     mid model. Mostly annotation of git-pkgs/usage output:
                     "this call passes user input", "this relies on ordering".

    hyrum-history    ./git-log.txt + ./prs.json + ./changelog.json + ./vulns.json
                     → breaks.json [{commit, symbol, what_changed, source}]
                     mid model. Current AGENT_SPEC phase 4.

    hyrum-generate   usage-surface.json + breaks.json + dep-outline.md
                     → tests/hyrum/<dep>/from_<target>/test_*.{py,go,js,rb}
                     high model. Current AGENT_SPEC phases 5-6 body lands here
                     near-verbatim, plus the test-pattern template.

    hyrum-validate   baseline-run.json + candidate-run.json + test files
                     → verdict.json [{test, status: real_break|brittle|env}]
                     mid model. Prunes brittle tests before commit.
                     Includes the assertion-quality gate (see below).

### Assertion-quality gate (borrowed from dotnet/skills)

Run inside hyrum-validate after tests are generated, before the version-matrix
run. For each generated test, flag when the only assertion is existence
(`hasattr`, `is not None`, `toBeDefined`, `callable(x)`) or a tautological
round-trip. The PoC's `test_api_exists` anti-pattern and the
`JSONIFY_PRETTYPRINT_REGULAR` miss are both this failure. Flagged tests go
back to hyrum-generate with a "assert on concrete output, not shape" prompt.

The pseudo-mutation half ("would inverting this condition slip past the
test?") is a second pass on the same gate: for each test, ask whether the
assertion would still pass if the dep returned an empty value / opposite
boolean / different type. Tests that survive mutation-in-thought get kept;
tests that don't get strengthened or dropped.

Third heuristic, from NTCoding/claude-skillz writing-tests: "assertion must
match test title". If the test is named `returns_uppercase_method` the
assertion must compare to an uppercase literal, not check `isinstance(str)`.
Cheaper to evaluate than pseudo-mutation and catches a different failure.

### Anti-pattern to keep out of hyrum-generate

BugMagnet-style exhaustive input-space probing (NaN, 10K-char strings, DST
boundaries, leap years) is the wrong direction here. hyrum-generate must
test only inputs the target is observed to pass. A test for
`jsonify(float('nan'))` when the target never does that is a false-positive
generator. The SKILL.md needs an explicit "do not invent inputs the target
doesn't use" rule with this as the counter-example, because generic
test-writing guidance will pull the model toward it.

hyrum-history gets one addition from the same source: bug clustering. When a
past break is found on symbol X, list sibling symbols in the same module as
candidates even if the target doesn't call them yet.

### downstream: one new mode

`downstream test --suite hyrum` runs `tests/hyrum/<upstream>/**` in each
dependent's checkout instead of the dependent's own test command. Everything
else (discover via ecosyste.ms, clone, replace, report) is unchanged. This is
the producer-corpus runner.

## Pipeline per dependency

    clone.Ensure(target)
    brief(target)                          → toolchain, test_cmd
    manifests/resolve(target)              → [dep purls]
    for dep in deps:
      registries.Lookup(dep)               → repo_url, versions
      clone.Ensure(dep repo)
      outline.Pack(dep repo)               → dep-outline.md
      usage.Index(target, dep)             → usage.json
      forge.SearchPRs(target, dep.name)    → prs.json
      git log --grep dep.name              → git-log.txt
      changelog.Parse(dep repo/CHANGELOG)  → changelog.json
      vulns.Lookup(dep)                    → vulns.json
      harness.Run(hyrum-usage)             → usage-surface.json
      harness.Run(hyrum-history)           → breaks.json
      harness.Run(hyrum-generate)          → tests/hyrum/<dep>/from_<target>/*
      harness.Run(hyrum-validate, gate=assertion-quality)
      managers.Install(dep@baseline); run tests → baseline-run.json
      managers.Install(dep@latest);   run tests → candidate-run.json
      harness.Run(hyrum-validate, classify)     → verdict.json
      write meta.json

## Strategy tiers (from dotnet/skills)

    direct       --dep given explicitly: skip discovery, run pipeline for
                 that one dep. hyrum-usage and hyrum-history can merge into
                 the generate prompt for small deps.
    single-pass  default: all direct deps, one pipeline each, sequential.
    iterative    --tree: resolve full graph, prune to deps reachable from
                 usage.Index, cost-estimate, then run. Re-run hyrum-generate
                 on deps where verdict.json flagged >N brittle tests.

## Output contract

    tests/hyrum/
      <upstream>/
        from_<target>/
          test_*.{py,go,js,rb,...}
          meta.json    {generated_at, target_sha, dep_purl, dep_baseline_version,
                        usage_surface_sha, skill_versions{}, harness_backend}
        README.md      aggregate: contributing downstreams, symbol coverage

meta.json makes regeneration idempotent (skip if inputs unchanged) and lets
`hyrum check` know which baseline each file was generated against.

## What's deliberately deferred

Runtime instrumentation. The PoC's Python tracers added type-contract detail
(request.method returns uppercase str) but the 60-70% detection came mostly
from static + history. Per-ecosystem tracers can be added later as optional
inputs to hyrum-usage; git-pkgs has no cross-ecosystem equivalent and building
one is a project on its own.

Web UI / DB. scrutineer has both; hyrum v1 is CLI + files only. The corpus
use case might eventually want a registry, but that's after the generation
loop is proven.

## First cut

npm only. managers/resolve/downstream support is most complete there and the
ws PoC tests exist as a fixture to diff against. Target: something with 5-10
direct deps and a test suite brief can find (candidate: octobox's node bits,
or engine.io itself since the ws tests were derived from it).

Milestones:

1. git-pkgs/usage with npm support, `hyrum surface` wired to it. No LLM.
   Prove the used/exported table on engine.io→ws matches the PoC's
   hand-built usage list.
2. cmd/hyrum scaffold + hyrum.yaml + harness wiring copied from
   scrutineer/internal/worker. `hyrum gen --dep pkg:npm/ws` runs
   hyrum-generate end to end and writes test files.
3. hyrum-validate with the assertion-quality gate. Run it on the PoC's
   existing httpbin tests and count how many it flags as trivial; that
   number is the gate's first eval.
4. `hyrum check` against ws@8.17.1 vs ws@8.21.3. Output should match what
   running the PoC tests by hand shows.
5. `downstream --suite hyrum` mode. Run engine.io's generated ws tests
   from inside a ws checkout.
6. Add Python (pip/uv). Regenerate httpbin→flask and diff against the PoC
   suite; overlap % is the second eval.
7. Add Go. Regenerate gin tests, same diff.

Each milestone is independently demoable. 1 and 3 have no LLM cost.

## Prior art for the SKILL.md bodies

Generic TDD/testing guides (Beck, GOOS, xUnit Patterns, BugMagnet) mostly
don't transfer: they assume you own the code under test and should explore
its input space. Hyrum's tests invert both. Relevant sources:

- Consumer-driven contracts (Ian Robinson 2006; Pact docs "what to include
  in a contract"). Same idea for HTTP; closest existing guidance on scope.
- Characterization tests (Feathers, WELC ch. 13). Record current behavior
  without asserting correctness; exactly the Hyrum's stance.
- Approval/golden testing (ApprovalTests). Tooling pattern for "assert
  concrete output".
- Producer-side API diffing: japicmp, revapi, cargo-semver-checks, Elm's
  enforced semver, Inria BreakBot. Hyrum's tests are the consumer-side
  complement that catches what signature diffs miss.
- SWE at Google ch. 21; hyrumslaw.com.

The PoC's own analysis/retrospective_analysis.md and analysis/learnings.md
are the primary source for hyrum-generate's anti-pattern list; they document
real failures on this exact task.
