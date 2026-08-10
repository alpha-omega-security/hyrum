# Usage-surface analysis without generating tests

`hyrum surface` reports which of a dependency's exports the target actually
imports, with call-site locations, without any model calls.

## CVE reachability

An OSV or GHSA advisory names affected versions and, in most records, the
functions or files the fix touched. Cross-referencing that against the
target's usage surface distinguishes an advisory affecting code on your call
path from one affecting an export the target does not import.

```
hyrum surface --dep lodash --json . | jq -r '.[0].symbols[].name'
```

If the advisory concerns `_.template` and the surface lists only `_.merge`
and `_.cloneDeep`, the vulnerable code path is not statically reachable from
this repository. That is a weaker guarantee than
[govulncheck](https://go.dev/blog/vuln) gives for Go, because dynamic dispatch
in JavaScript or Python can reach code the static extractor misses; the LLM
`hyrum-usage` step traces through instances for a tighter bound when it
matters.

## Vendoring and removal candidates

The summary view sorts by call-site count:

```
$ hyrum surface .
DEP           ECOSYSTEM  VERSION  SCOPE    SYMBOLS  SITES  INDEX
Flask         pypi                runtime  22       72     ok
six           pypi                runtime  6        8      ok
decorator     pypi                runtime  1        1      ok
MarkupSafe    pypi                runtime  0        0      ok
itsdangerous  pypi                runtime  0        0      ok
```

`decorator` at one symbol and one call site is a vendoring or removal
candidate: inlining one function is often cheaper than carrying the
dependency. `MarkupSafe` and `itsdangerous` at zero are transitive
dependencies of Flask that this project does not import directly, so they are
Flask's concern. `hyrum surface --dep decorator .` prints the one call site to
inspect before deciding.

For `gen` runs where the dependency's source has been cloned and outlined,
`context.json` also carries `exported_symbols`, so the ratio of used to
exported is available (engine.io uses 15 of ws's 23 exported names; a project
using 2 of 300 is a stronger cut candidate).
