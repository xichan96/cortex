package dino

import "testing"

func TestProviderRetry(t *testing.T) {
	if got := providerRetry(nil); got != nil {
		t.Fatalf("providerRetry(nil) = %+v, want nil", got)
	}

	cfg := &Config{}
	if got := providerRetry(cfg); got != nil {
		t.Fatalf("unset retry = %+v, want nil (provider default)", got)
	}

	zero := 0
	cfg.Provider.RetryMaxRetries = &zero
	got := providerRetry(cfg)
	if got == nil || got.MaxRetries != 0 {
		t.Fatalf("retry 0 = %+v, want MaxRetries 0 (disabled)", got)
	}

	two := 2
	cfg.Provider.RetryMaxRetries = &two
	got = providerRetry(cfg)
	if got == nil || got.MaxRetries != 2 {
		t.Fatalf("retry 2 = %+v, want MaxRetries 2", got)
	}
}
