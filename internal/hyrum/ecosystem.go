package hyrum

// PURL type strings for the ecosystems hyrum supports. These are the values
// git-pkgs/purl parses into PURL.Type and that git-pkgs/manifests emits; they
// are also the keys used to register usage indexers, test runners, and
// package managers.
const (
	EcoNPM      = "npm"
	EcoPyPI     = "pypi"
	EcoGo       = "golang"
	EcoGem      = "gem"
	EcoCargo    = "cargo"
	EcoComposer = "composer"
	EcoHex      = "hex"
)
