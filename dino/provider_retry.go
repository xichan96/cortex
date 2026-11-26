package dino

import "github.com/xichan96/cortex/agent/model"

// providerRetry translates the YAML-facing retry knob into a provider retry
// policy. nil leaves the provider default (one retry) in place; an explicit 0
// disables retries so a host pointing at its own gateway does not retry twice.
func providerRetry(cfg *Config) *model.TransportRetryPolicy {
	if cfg == nil || cfg.Provider.RetryMaxRetries == nil {
		return nil
	}
	return &model.TransportRetryPolicy{MaxRetries: *cfg.Provider.RetryMaxRetries}
}
