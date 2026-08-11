package skills_test

import (
	"testing"

	"github.com/alpha-omega-security/hyrum/skills"
)

func TestEmbedIncludesValidate(t *testing.T) {
	for _, name := range []string{"hyrum-usage", "hyrum-history", "hyrum-generate", "hyrum-validate"} {
		if _, err := skills.FS.ReadFile(name + "/SKILL.md"); err != nil {
			t.Errorf("%s/SKILL.md: %v", name, err)
		}
		if _, err := skills.FS.ReadFile(name + "/schema.json"); err != nil {
			t.Errorf("%s/schema.json: %v", name, err)
		}
	}
}
