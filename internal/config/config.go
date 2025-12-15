package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

var globalConfig *Config

func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	globalConfig = &cfg
	return nil
}

func Get() *Config {
	return globalConfig
}
