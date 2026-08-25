# Running dependents' contract tests before a release

A library maintainer preparing a release usually cannot run every dependent's
test suite: cloning, installing, and building N downstream projects takes
hours and fails for reasons unrelated to the change. Hyrum's tests are
hermetic and import only the library under test, so a corpus of them from many
dependents runs in seconds against a local checkout.

## Building a corpus

```
hyrum corpus --upstream ws --out ./corpus --backend codex --run \
  --dependent https://github.com/socketio/engine.io@6.5.4 \
  --dependent https://github.com/vercel/next.js \
  --dependent https://github.com/apollographql/apollo-server
```

Each `--dependent` is cloned, its usage of `ws` is extracted, and a test file
is written under `./corpus/ws/from_<dependent>/`. The dependent component uses
one root manifest's package name. Nested targets add a suffix derived from the
Git-relative path; targets without one package name use the repository name and
relative path. The `@ref` suffix pins a dependent
that has moved or been archived. Dependents can also be listed in a
`downstream.toml` written by
[downstream discover](https://github.com/git-pkgs/downstream), which queries
[ecosyste.ms](https://ecosyste.ms) for the most-used packages depending on
`ws` and filters out forks and archived repos.

## Running the corpus against a change

From the library checkout, install the local build and run the corpus:

```
cd ws
npm pack                                       # or go build, cargo build, ...
npm install --no-save ./ws-8.22.0.tgz
node --test ../corpus/ws/**/*.test.js
```

A failing test names which dependent depends on the changed behaviour and
where in that dependent's code. A clean run against a security patch is
evidence that the patch changed no observable behaviour for the sampled
dependents, and the failure list on an intentional break is the set of
downstream call sites that will need adapting.

Regenerating a single `from_<dependent>` directory is one `hyrum gen`
invocation with `--out ./corpus`, so the corpus can be committed alongside the
library and a stale contribution refreshed without rebuilding the whole set.

## Compared to running dependents' own suites

[downstream](https://github.com/git-pkgs/downstream) runs each dependent's
actual test command against a replaced upstream, which catches everything
including build breaks but takes minutes per dependent and requires each
dependent's toolchain. A Hyrum's corpus trades that completeness for speed and
for isolating dependency behaviour from the dependent's own bugs. Running both
is reasonable: the corpus on every commit, `downstream test` before a release.
