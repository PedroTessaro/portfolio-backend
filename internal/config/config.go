// Package config loads the terminal's editorial content from YAML, so changing
// the bio or the stack doesn't mean rebuilding the binary.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Identity struct {
	Name       string `yaml:"name"`
	Role       string `yaml:"role"`
	Location   string `yaml:"location"`
	GitHubUser string `yaml:"github_user"`
}

type Terminal struct {
	Host    string `yaml:"host"`
	User    string `yaml:"user"`
	Machine string `yaml:"machine"`
}

type Config struct {
	Identity Identity `yaml:"identity"`
	Stack    []string `yaml:"stack"`
	Terminal Terminal `yaml:"terminal"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
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
