module github.com/alpha-omega-security/hyrum

go 1.26

require (
	github.com/alpha-omega-security/harness v0.0.0
	github.com/git-pkgs/brief v0.0.0
	github.com/git-pkgs/clone v0.0.0
	github.com/git-pkgs/outline v0.1.8
	github.com/git-pkgs/purl v0.1.15
	github.com/git-pkgs/registries v0.6.4
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/bazelbuild/buildtools v0.0.0-20260716142318-04cf7de1434f // indirect
	github.com/bmatcuk/doublestar/v4 v4.10.0 // indirect
	github.com/git-pkgs/gitignore v1.2.0 // indirect
	github.com/git-pkgs/licensecheck v0.4.1 // indirect
	github.com/git-pkgs/magic v0.2.0 // indirect
	github.com/git-pkgs/manifests v0.7.0 // indirect
	github.com/git-pkgs/pom v0.1.5 // indirect
	github.com/git-pkgs/spdx v0.3.0 // indirect
	github.com/git-pkgs/vers v0.3.0 // indirect
	github.com/github/go-spdx/v2 v2.7.0 // indirect
	github.com/odvcencio/gotreesitter v0.47.0 // indirect
	github.com/package-url/packageurl-go v0.1.6 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Spike: use local checkouts under ~/code so API drift is visible immediately.
// Drop these once tagged versions are pinned.
replace (
	github.com/alpha-omega-security/harness => ../harness
	github.com/git-pkgs/brief => ../../git-pkgs/brief
	github.com/git-pkgs/clone => ../../git-pkgs/clone
	github.com/git-pkgs/manifests => ../../git-pkgs/manifests
	github.com/git-pkgs/outline => ../../git-pkgs/outline
	github.com/git-pkgs/registries => ../../git-pkgs/registries
)
