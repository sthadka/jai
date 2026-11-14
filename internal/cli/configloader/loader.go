package configloader

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sthadka/jai/pkg/config"
	"gopkg.in/yaml.v3"
)

var envVarRe = regexp.MustCompile(`\$\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

func substituteEnvVars(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(m string) string {
		key := m[2 : len(m)-1] // strip ${ and }
		if val, ok := os.LookupEnv(key); ok {
			return val
		}
		return m
	})
}

// Load reads and parses the config file at path.
func Load(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

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
