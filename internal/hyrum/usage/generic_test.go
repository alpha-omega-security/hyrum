package usage

import "testing"

func TestGoImportMatch(t *testing.T) {
	dep := "github.com/gin-contrib/sse"
	yes := []string{
		`	"github.com/gin-contrib/sse"`,
		`	sse "github.com/gin-contrib/sse"`,
		`import "github.com/gin-contrib/sse/v2"`,
	}
	no := []string{
		`	"github.com/gin-contrib/sse-other"`,
		`// see github.com/gin-contrib/sse`,
		`	"github.com/gin-contrib/ssex"`,
	}
	for _, l := range yes {
		if !goImportMatch(l, dep) {
			t.Errorf("should match: %q", l)
		}
	}
	for _, l := range no {
		if goImportMatch(l, dep) {
			t.Errorf("should not match: %q", l)
		}
	}
}

func TestRubyRequireMatch(t *testing.T) {
	yes := []string{
		`require 'octokit'`,
		`require "octokit"`,
		`require 'octokit/client'`,
		`rescue Octokit::NotFound`,
		`client = Octokit::Client.new(token: t)`,
		`Octokit.configure { |c| c.api = api }`,
	}
	no := []string{
		`require 'octokitx'`,
		`# Octokitten`,
		`MyOctokit::Client`,
	}
	for _, l := range yes {
		if !rubyRequireMatch(l, "octokit") {
			t.Errorf("should match: %q", l)
		}
	}
	for _, l := range no {
		if rubyRequireMatch(l, "octokit") {
			t.Errorf("should not match: %q", l)
		}
	}
}

func TestRubyCamelize(t *testing.T) {
	cases := map[string]string{
		"octokit":        "Octokit",
		"active_support": "ActiveSupport",
		"faraday-retry":  "FaradayRetry",
		"oj":             "Oj",
	}
	for in, want := range cases {
		if got := rubyCamelize(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestRustUseMatch(t *testing.T) {
	yes := []string{
		`use serde::Deserialize;`,
		`use serde;`,
		`extern crate serde;`,
	}
	no := []string{
		`use serde_json::Value;`,
		`// use serde`,
	}
	for _, l := range yes {
		if !rustUseMatch(l, "serde") {
			t.Errorf("should match: %q", l)
		}
	}
	for _, l := range no {
		if rustUseMatch(l, "serde") {
			t.Errorf("should not match: %q", l)
		}
	}
	// Hyphen → underscore
	if !rustUseMatch(`use tokio_util::codec;`, "tokio-util") {
		t.Error("hyphen/underscore not normalised")
	}
}

func TestPhpUseMatch(t *testing.T) {
	if !phpUseMatch(`use GuzzleHttp\Client;`, "guzzlehttp/guzzle") {
		t.Error("PSR-4 vendor namespace not matched")
	}
	if phpUseMatch(`use App\Models\User;`, "guzzlehttp/guzzle") {
		t.Error("unrelated namespace matched")
	}
}

func TestElixirMatch(t *testing.T) {
	yes := []string{
		`alias Jason.Encoder`,
		`import Jason`,
		`use Jason, only: [:encode]`,
	}
	for _, l := range yes {
		if !elixirMatch(l, "jason") {
			t.Errorf("should match: %q", l)
		}
	}
	if elixirMatch(`alias JasonExtra.Foo`, "jason") {
		t.Error("prefix over-match")
	}
	if !elixirMatch(`alias PhoenixHtml.Safe`, "phoenix_html") {
		t.Error("underscore camelize")
	}
}
