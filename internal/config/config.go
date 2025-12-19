package config

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

var (
	globalConfig *Config
	configMu     sync.RWMutex
)

func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	configMu.Lock()
	globalConfig = &cfg
	configMu.Unlock()
	return nil
}

func Get() *Config {
	configMu.RLock()
	defer configMu.RUnlock()
	return globalConfig
}
