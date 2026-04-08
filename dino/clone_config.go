package dino

import (
	"encoding/json"
	"fmt"
)

func CloneConfig(cfg *Config) (*Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil dino config")
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var out Config
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
