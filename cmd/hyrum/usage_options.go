package main

import (
	"fmt"
	"path/filepath"

	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
)

func resolveUsageOptions(
	scopes, includes, excludes stringList,
	defaultScopes []usage.Scope,
) (usage.IndexOptions, error) {
	opts := usage.IndexOptions{
		Scopes: append([]usage.Scope(nil), defaultScopes...),
	}
	if len(scopes) > 0 {
		opts.Scopes = nil
		for _, value := range scopes {
			scope, err := resolveUsageScope(value)
			if err != nil {
				return usage.IndexOptions{}, err
			}
			opts.Scopes = append(opts.Scopes, scope)
		}
	}
	for _, value := range includes {
		path, err := resolveUsagePath("include path", value)
		if err != nil {
			return usage.IndexOptions{}, err
		}
		opts.IncludePaths = append(opts.IncludePaths, path)
	}
	for _, value := range excludes {
		path, err := resolveUsagePath("exclude path", value)
		if err != nil {
			return usage.IndexOptions{}, err
		}
		opts.ExcludePaths = append(opts.ExcludePaths, path)
	}
	return opts, nil
}

func resolveUsageScope(value string) (usage.Scope, error) {
	switch scope := usage.Scope(value); scope {
	case usage.ScopeProduction, usage.ScopeTest, usage.ScopeExample, usage.ScopeDocumentation:
		return scope, nil
	default:
		return "", fmt.Errorf("unknown usage scope %q (want production, test, example, or documentation)", value)
	}
}

func resolveUsagePath(label, value string) (string, error) {
	clean := filepath.Clean(value)
	if err := validateRelativePath(label, clean); err != nil {
		return "", err
	}
	return filepath.ToSlash(clean), nil
}
