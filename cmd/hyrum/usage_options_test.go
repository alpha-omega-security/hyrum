package main

import (
	"reflect"
	"testing"

	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
)

func TestResolveUsageOptionsUsesDefaultScopes(t *testing.T) {
	defaults := []usage.Scope{usage.ScopeProduction}
	opts, err := resolveUsageOptions(nil, nil, nil, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opts.Scopes, defaults) {
		t.Errorf("scopes = %v, want %v", opts.Scopes, defaults)
	}

	opts.Scopes[0] = usage.ScopeTest
	if defaults[0] != usage.ScopeProduction {
		t.Error("resolved options changed the caller's default scopes")
	}
}

func TestResolveUsageOptionsUsesExplicitScopesAndPaths(t *testing.T) {
	opts, err := resolveUsageOptions(
		stringList{"test", "example"},
		stringList{"./src/"},
		stringList{"src/generated/"},
		[]usage.Scope{usage.ScopeProduction},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantScopes := []usage.Scope{usage.ScopeTest, usage.ScopeExample}
	if !reflect.DeepEqual(opts.Scopes, wantScopes) {
		t.Errorf("scopes = %v, want %v", opts.Scopes, wantScopes)
	}
	if !reflect.DeepEqual(opts.IncludePaths, []string{"src"}) {
		t.Errorf("include paths = %v", opts.IncludePaths)
	}
	if !reflect.DeepEqual(opts.ExcludePaths, []string{"src/generated"}) {
		t.Errorf("exclude paths = %v", opts.ExcludePaths)
	}
}

func TestResolveUsageOptionsRejectsUnknownScopeAndUnsafePath(t *testing.T) {
	if _, err := resolveUsageOptions(stringList{"benchmark"}, nil, nil, nil); err == nil {
		t.Error("unknown scope accepted")
	}
	if _, err := resolveUsageOptions(nil, stringList{"../outside"}, nil, nil); err == nil {
		t.Error("traversing include path accepted")
	}
	if _, err := resolveUsageOptions(nil, nil, stringList{"/absolute"}, nil); err == nil {
		t.Error("absolute exclude path accepted")
	}
}
