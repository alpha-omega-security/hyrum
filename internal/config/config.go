// Package config parses and validates hyrum.yaml independently from command
// execution so the same configuration contract can be reused by other
// subcommands as they gain configuration support.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	Filename      = "hyrum.yaml"
	yamlStringTag = "!!str"
	yamlBoolTag   = "!!bool"
	yamlIntTag    = "!!int"
)

// File is the typed representation of hyrum.yaml. Pointer scalar fields
// preserve the distinction between an omitted setting and an explicit value.
type File struct {
	Backend      *string               `yaml:"backend,omitempty"`
	Models       map[string]string     `yaml:"models,omitempty"`
	Out          *string               `yaml:"out,omitempty"`
	Work         *string               `yaml:"work,omitempty"`
	OutlineBytes *int                  `yaml:"outline_bytes,omitempty"`
	Deps         map[string]Dependency `yaml:"deps,omitempty"`
}

// Dependency contains settings for one dependency, keyed by its manifest
// name or full purl in File.Deps.
type Dependency struct {
	Baseline    *string  `yaml:"baseline,omitempty"`
	Skip        *bool    `yaml:"skip,omitempty"`
	Activations []string `yaml:"activations,omitempty"`
}

var modelSkills = map[string]bool{
	"hyrum-usage":    true,
	"hyrum-history":  true,
	"hyrum-generate": true,
	"hyrum-validate": true,
}

var modelTiers = map[string]bool{
	"mid":  true,
	"high": true,
	"max":  true,
}

// Load reads an explicitly requested config, or discovers hyrum.yaml exactly
// at targetRoot. An absent discovered file is optional; every error for an
// explicit file is returned. source is the absolute path of the loaded file.
func Load(targetRoot, explicit string) (cfg File, source string, err error) {
	var path string
	if explicit != "" {
		path, err = ExpandUser(explicit)
		if err != nil {
			return File{}, "", fmt.Errorf("config path: %w", err)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return File{}, "", fmt.Errorf("config path: %w", err)
		}
	} else {
		root, absErr := filepath.Abs(targetRoot)
		if absErr != nil {
			return File{}, "", fmt.Errorf("target path: %w", absErr)
		}
		path = filepath.Join(root, Filename)
	}

	b, readErr := os.ReadFile(path)
	if readErr != nil {
		if explicit == "" && errors.Is(readErr, os.ErrNotExist) {
			return File{}, "", nil
		}
		return File{}, "", fmt.Errorf("read config %q: %w", path, readErr)
	}
	cfg, err = Parse(b)
	if err != nil {
		return File{}, "", fmt.Errorf("parse config %q: %w", path, err)
	}
	return cfg, filepath.Clean(path), nil
}

// Parse strictly decodes one YAML document. Unknown fields, nulls, type
// mismatches, unsupported model settings, and empty scalar values fail closed.
func Parse(data []byte) (File, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return File{}, err
	}
	if err := rejectNulls(&root); err != nil {
		return File{}, err
	}
	if err := validateNodeTypes(&root); err != nil {
		return File{}, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg File
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return File{}, nil
		}
		return File{}, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return File{}, fmt.Errorf("multiple YAML documents are not supported")
		}
		return File{}, err
	}
	if err := cfg.validate(); err != nil {
		return File{}, err
	}
	return cfg, nil
}

func (cfg File) validate() error {
	for name, value := range map[string]*string{
		"backend": cfg.Backend,
		"out":     cfg.Out,
		"work":    cfg.Work,
	} {
		if value != nil && strings.TrimSpace(*value) == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
	}
	if cfg.OutlineBytes != nil && *cfg.OutlineBytes <= 0 {
		return fmt.Errorf("outline_bytes must be greater than zero")
	}
	for skill, tier := range cfg.Models {
		if !modelSkills[skill] {
			return fmt.Errorf("models.%s is unsupported", skill)
		}
		if !modelTiers[tier] {
			return fmt.Errorf("models.%s has unsupported tier %q (want mid, high, or max)", skill, tier)
		}
	}
	for key, dep := range cfg.Deps {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("deps contains an empty dependency key")
		}
		if dep.Baseline != nil && strings.TrimSpace(*dep.Baseline) == "" {
			return fmt.Errorf("deps.%s.baseline must not be empty", key)
		}
		for i, activation := range dep.Activations {
			if strings.TrimSpace(activation) == "" {
				return fmt.Errorf("deps.%s.activations[%d] must not be empty", key, i)
			}
		}
	}
	return nil
}

func rejectNulls(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Tag == "!!null" {
		return fmt.Errorf("null values are not supported (line %d)", node.Line)
	}
	for _, child := range node.Content {
		if err := rejectNulls(child); err != nil {
			return err
		}
	}
	return nil
}

func validateNodeTypes(root *yaml.Node) error {
	if root == nil || len(root.Content) == 0 {
		return nil
	}
	node := root.Content[0]
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("configuration must be a mapping (line %d)", node.Line)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag != yamlStringTag {
			return fmt.Errorf("configuration keys must be strings (line %d)", key.Line)
		}
		switch key.Value {
		case "backend", "out", "work":
			if err := requireTag(key.Value, value, yamlStringTag); err != nil {
				return err
			}
		case "outline_bytes":
			if err := requireTag(key.Value, value, yamlIntTag); err != nil {
				return err
			}
		case "models":
			if err := validateStringMap("models", value); err != nil {
				return err
			}
		case "deps":
			if err := validateDependencies(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStringMap(path string, node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s must be a mapping (line %d)", path, node.Line)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag != yamlStringTag {
			return fmt.Errorf("%s keys must be strings (line %d)", path, key.Line)
		}
		if err := requireTag(path+"."+key.Value, value, yamlStringTag); err != nil {
			return err
		}
	}
	return nil
}

func validateDependencies(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("deps must be a mapping (line %d)", node.Line)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag != yamlStringTag {
			return fmt.Errorf("deps keys must be strings (line %d)", key.Line)
		}
		if value.Kind != yaml.MappingNode {
			return fmt.Errorf("deps.%s must be a mapping (line %d)", key.Value, value.Line)
		}
		for j := 0; j < len(value.Content); j += 2 {
			field, fieldValue := value.Content[j], value.Content[j+1]
			if field.Tag != yamlStringTag {
				return fmt.Errorf("deps.%s keys must be strings (line %d)", key.Value, field.Line)
			}
			switch field.Value {
			case "baseline":
				if err := requireTag("deps."+key.Value+".baseline", fieldValue, yamlStringTag); err != nil {
					return err
				}
			case "skip":
				if err := requireTag("deps."+key.Value+".skip", fieldValue, yamlBoolTag); err != nil {
					return err
				}
			case "activations":
				if err := validateStringSequence("deps."+key.Value+".activations", fieldValue); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateStringSequence(path string, node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s must be a sequence (line %d)", path, node.Line)
	}
	for i, value := range node.Content {
		if err := requireTag(fmt.Sprintf("%s[%d]", path, i), value, yamlStringTag); err != nil {
			return err
		}
	}
	return nil
}

func requireTag(path string, node *yaml.Node, tag string) error {
	if node.Kind == yaml.AliasNode {
		return fmt.Errorf("%s aliases are not supported (line %d)", path, node.Line)
	}
	if node.Tag != tag {
		switch {
		case tag == yamlStringTag && strings.HasSuffix(path, ".baseline"):
			return fmt.Errorf("%s must be a quoted string (got %s at line %d)", path, node.Tag, node.Line)
		case tag == yamlStringTag:
			return fmt.Errorf("%s must be a string (got %s at line %d)", path, node.Tag, node.Line)
		case tag == yamlBoolTag:
			return fmt.Errorf("%s must be true or false (got %s at line %d)", path, node.Tag, node.Line)
		case tag == yamlIntTag:
			return fmt.Errorf("%s must be an integer (got %s at line %d)", path, node.Tag, node.Line)
		default:
			return fmt.Errorf("%s has the wrong type %s (line %d)", path, node.Tag, node.Line)
		}
	}
	return nil
}

// ResolvePath expands a configured path and resolves relative values from the
// directory containing the loaded config file.
func ResolvePath(source, value string) (string, error) {
	expanded, err := ExpandUser(value)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded), nil
	}
	if source == "" {
		return "", fmt.Errorf("cannot resolve relative configured path %q without a config file", value)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), expanded)), nil
}

// ExpandUser expands a leading ~ or ~/ using the current user's home. Other
// users' home directories (~name) are intentionally not inferred.
func ExpandUser(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		if strings.HasPrefix(path, "~") {
			return "", fmt.Errorf("unsupported home path %q (only ~ and ~/ are expanded)", path)
		}
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"+string(filepath.Separator))), nil
}
