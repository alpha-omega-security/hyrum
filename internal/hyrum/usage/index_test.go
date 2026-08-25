package usage

import (
	"context"
	"errors"
	"testing"
)

func TestWalkSourceFilesStopsAfterCancellation(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.py": "import flask\n",
		"b.py": "import flask\n",
	})
	ctx, cancel := context.WithCancel(t.Context())
	visited := 0
	err := walkSourceFiles(ctx, root, set(".py"), func(string) error {
		visited++
		cancel()
		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("walk error = %v, want context canceled", err)
	}
	if visited != 1 {
		t.Fatalf("visited %d files after cancellation, want 1", visited)
	}
}

func TestScanReturnsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := scanWithOptions(ctx, t.TempDir(), specs["pypi"], nil, nil, IndexOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("scan error = %v, want context canceled", err)
	}
}

func TestGoIndex(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.go": "package main\n" +
			"import (\n" +
			"\t\"github.com/gin-contrib/sse\"\n" +
			"\ts2 \"github.com/gin-contrib/sse/v2\"\n" +
			"\t\"github.com/gin-contrib/sse-other\"\n" +
			")\n" +
			"func f() { sse.Encode(w, sse.Event{}); s2.Handler() }\n",
	})
	s, err := Index(t.Context(), root, "pkg:golang/github.com/gin-contrib/sse")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["github.com/gin-contrib/sse"] == 0 || got["github.com/gin-contrib/sse/v2"] == 0 {
		t.Errorf("module and subpath imports: %v", got)
	}
	if got["github.com/gin-contrib/sse.Encode"] == 0 || got["github.com/gin-contrib/sse.Event"] == 0 {
		t.Errorf("selector refs on implicit package name: %v", got)
	}
	if got["github.com/gin-contrib/sse/v2.Handler"] == 0 {
		t.Errorf("selector ref on aliased import: %v", got)
	}
	if _, ok := got["github.com/gin-contrib/sse-other"]; ok {
		t.Errorf("prefix over-match: %v", got)
	}
}

func TestGemIndex(t *testing.T) {
	// No require line: Rails apps rely on Bundler.require, so the constant
	// is the only signal.
	root := writeTree(t, map[string]string{
		"app.rb": "require 'octokit/client'\n" +
			"client = Octokit::Client.new(token: t)\n" +
			"Octokit.configure { |c| c.api = api }\n" +
			"MyOctokit::Client.new\n",
	})
	s, err := Index(t.Context(), root, "pkg:gem/octokit")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["octokit/client"] == 0 {
		t.Errorf("require subpath: %v", got)
	}
	if got["Octokit.Client"] == 0 || got["Octokit.configure"] == 0 {
		t.Errorf("constant refs via seed receiver: %v", got)
	}
	if _, ok := got["MyOctokit.Client"]; ok {
		t.Errorf("prefixed constant leaked: %v", got)
	}
}

func TestGemCamelize(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.rb": "ActiveSupport::Duration.parse(s)\n",
	})
	s, err := Index(t.Context(), root, "pkg:gem/active_support")
	if err != nil {
		t.Fatal(err)
	}
	if symbolNames(s)["ActiveSupport.Duration"] == 0 {
		t.Errorf("underscore→camel constant: %v", symbolNames(s))
	}
}

func TestCargoIndex(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/lib.rs": "use serde::{Deserialize, Serialize as Ser};\n" +
			"use serde_json::Value;\n" + // must not match "serde"
			"fn f() -> Ser { serde::to_string(&x) }\n",
	})
	s, err := Index(t.Context(), root, "pkg:cargo/serde")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["Deserialize"] == 0 || got["Serialize"] == 0 {
		t.Errorf("named use items: %v", got)
	}
	if got["serde.to_string"] == 0 {
		t.Errorf("crate path ref via seed: %v", got)
	}
	for name := range got {
		if name == "Value" {
			t.Errorf("serde_json leaked into serde: %v", got)
		}
	}
}

func TestCargoHyphenUnderscore(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/lib.rs": "use tokio_util::codec::Framed;\n",
	})
	s, err := Index(t.Context(), root, "pkg:cargo/tokio-util")
	if err != nil {
		t.Fatal(err)
	}
	if len(symbolNames(s)) == 0 {
		t.Errorf("hyphen→underscore crate name not matched: %v", symbolNames(s))
	}
}

func TestComposerIndex(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/App.php": "<?php\n" +
			"use GuzzleHttp\\Client;\n" +
			"use App\\Models\\User;\n" +
			"$c = new Client();\n",
	})
	s, err := Index(t.Context(), root, "pkg:composer/guzzlehttp/guzzle")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got[`GuzzleHttp\Client`] == 0 {
		t.Errorf("PSR-4 use statement: %v", got)
	}
	if _, ok := got[`App\Models\User`]; ok {
		t.Errorf("unrelated namespace leaked: %v", got)
	}
}

func TestHexIndex(t *testing.T) {
	root := writeTree(t, map[string]string{
		"lib/app.ex": "defmodule App do\n" +
			"  alias Jason.Encoder\n" +
			"  def f(x), do: Jason.encode!(x)\n" +
			"end\n",
	})
	s, err := Index(t.Context(), root, "pkg:hex/jason")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["Jason.Encoder"] == 0 {
		t.Errorf("alias directive: %v", got)
	}
	if got["Jason.encode!"] == 0 {
		t.Errorf("dotted call via seed: %v", got)
	}
}

func TestSiteContext(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.py": "  from flask import jsonify   # trailing\n",
	})
	s, err := Index(t.Context(), root, "pkg:pypi/flask")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Symbols) == 0 || len(s.Symbols[0].Sites) == 0 {
		t.Fatalf("no sites: %+v", s)
	}
	ctx := s.Symbols[0].Sites[0].Context
	if ctx != "from flask import jsonify   # trailing" {
		t.Errorf("context should be the trimmed source line, got %q", ctx)
	}
}
