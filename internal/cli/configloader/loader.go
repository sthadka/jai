// Package configloader reads jai configuration from a YAML file on disk,
// expands ${ENV} references, and returns the pure engine-policy
// github.com/sthadka/jai/v5/pkg/config.Config struct. File and environment access
// live here, in the CLI layer, so that pkg/config stays free of I/O and can be
// embedded by other Go applications that supply configuration however they like.
package configloader

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sthadka/jai/v5/pkg/config"
)

var envVarRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// substituteEnvVars replaces ${VAR} with environment variable values.
// Unset variables are left untouched so the error surfaces at validation.
func substituteEnvVars(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(m string) string {
		key := m[2 : len(m)-1] // strip ${ and }
		if val, ok := os.LookupEnv(key); ok {
			return val
		}
		return m
	})
}

// Load reads and parses the config file at path, expanding ${ENV} references
// before unmarshalling and resolving the database path (default or ~/ prefix).
func Load(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	// Substitute env vars before parsing YAML.
	expanded := substituteEnvVars(string(data))

	cfg := config.Defaults()
	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	if cfg.DB.Path == "" {
		cfg.DB.Path = config.DefaultDBPath()
	} else if strings.HasPrefix(cfg.DB.Path, "~/") {
		home, _ := os.UserHomeDir()
		cfg.DB.Path = filepath.Join(home, cfg.DB.Path[2:])
	}

	return cfg, nil
}
