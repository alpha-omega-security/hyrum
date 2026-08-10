# hyrum

Generate and run Hyrum's tests: hermetic tests that capture how a repository
actually calls each of its dependencies, so that a version bump which changes
observed behaviour fails CI before it reaches production.

Built on [harness](https://github.com/alpha-omega-security/harness) for the
LLM-driven steps and the [git-pkgs](https://github.com/git-pkgs) libraries for
toolchain detection, manifest parsing, registry lookup, cloning, and source
outlining.

Spike status. See `docs/design.md` for the plan and `docs/background.md` for
the use cases that motivated it.

## Build

```
go build ./cmd/hyrum
```

Requires Go 1.26 and, until the replace directives in `go.mod` are dropped,
sibling checkouts of `alpha-omega-security/harness` and the referenced
`git-pkgs/*` repos under `~/code`.

## Usage

```
hyrum surface <path>              # per-dep usage summary (no LLM)
hyrum surface <path> --dep ws     # symbol-level detail for one dep
hyrum gen <path> --dep ws         # stage generation context; --run to invoke backend
```

`hyrum check` and `hyrum corpus` are not implemented yet.
