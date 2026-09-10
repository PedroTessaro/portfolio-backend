// Package config holds the terminal's editorial content.
//
// The YAML is embedded rather than read from disk: on a serverless platform the
// function ships as a binary with no repository around it, and content changes
// mean a redeploy anyway. CONFIG_PATH still overrides it, which is what makes
// local iteration on the text quick.
package config

import (
	_ "embed"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

//go:embed profile.yaml
var embedded []byte

type Identity struct {
	Name       string `yaml:"name"`
	Role       string `yaml:"role"`
	Location   string `yaml:"location"`
	GitHubUser string `yaml:"github_user"`
	Tagline    string `yaml:"tagline"` // optional third line under the name
}

type Terminal struct {
	Host    string `yaml:"host"`
	User    string `yaml:"user"`
	Machine string `yaml:"machine"`
}

type Config struct {
	Identity Identity `yaml:"identity"`
	Stack    []string `yaml:"stack"`
	Projects []string `yaml:"projects"` // repo names to feature, resolved against the API
	Terminal Terminal `yaml:"terminal"`
}

// Load reads the config at path, or the embedded copy when path is empty.
func Load(path string) (*Config, error) {
	raw, source := embedded, "embedded profile.yaml"

	if path != "" {
		fromDisk, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		raw, source = fromDisk, path
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", source, err)
	}
	return &cfg, nil
}

// validate fails the boot instead of letting an empty field turn into a hole in
// the SVG that visitors get.
func (c *Config) validate() error {
	required := map[string]string{
		"identity.name":        c.Identity.Name,
		"identity.role":        c.Identity.Role,
		"identity.github_user": c.Identity.GitHubUser,
		"terminal.host":        c.Terminal.Host,
		"terminal.user":        c.Terminal.User,
		"terminal.machine":     c.Terminal.Machine,
	}
	for field, value := range required {
		if value == "" {
			return fmt.Errorf("%s is empty", field)
		}
	}
	if len(c.Stack) == 0 {
		return fmt.Errorf("stack is empty")
	}
	return nil
}
