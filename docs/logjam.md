# Explaining a pinned dependency

A `requirements.txt` line reading `werkzeug<3` or a package.json range
`"ws": "~7.4.2"` records that someone found the newer version broke
something without recording what, and after a few months the pin is still
there, the advisory scanner flags it, and the reason has been lost.

`hyrum gen` against the pinned version establishes a baseline suite; `hyrum
check` against the blocked version prints what fails.

```
$ hyrum gen --dep werkzeug --backend codex --run .
$ hyrum check --dep werkzeug@3.1.8 .
→ pip add werkzeug@3.1.8
werkzeug@3.1.8: FAIL
  ✖ test_parse_authorization_header_import
    ImportError: cannot import name 'parse_authorization_header' from 'werkzeug.http'
  ✖ test_www_authenticate_set_digest
    AttributeError: 'WWWAuthenticate' object has no attribute 'set_digest'
```

Each failure names a specific symbol and the target file that uses it (the
test's docstring cites `httpbin/helpers.py:16`). That is the concrete change
list for lifting the pin: replace `parse_authorization_header` with
`Authorization.from_header`, replace `set_digest` with a constructed
`WWWAuthenticate`, then remove the `<3`.

The same approach applies to a long-open dependabot PR: `hyrum check --dep
X@<proposed>` on the PR branch prints why merging would break, without needing
to remember or re-derive it.

For a dependency that is pinned but has no generated tests yet, `hyrum surface
--dep X .` gives the list of call sites to inspect by hand, which is often
enough on its own when the surface is small.
