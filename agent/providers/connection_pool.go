package providers

import (
	"net"
	"net/http"
	"sync"
	"time"
)

var (
	globalTransport *http.Transport
	transportOnce   sync.Once
)

type ConnectionPoolConfig struct {
	MaxSize     int
	IdleTimeout time.Duration
	DialTimeout time.Duration
	KeepAlive   time.Duration
}

func DefaultConnectionPoolConfig() ConnectionPoolConfig {
	return ConnectionPoolConfig{
		MaxSize:     15,
		IdleTimeout: 30 * time.Second,
		DialTimeout: 5 * time.Second,
		KeepAlive:   60 * time.Second,
	}
}

func GetGlobalTransport() *http.Transport {
	transportOnce.Do(func() {
		config := DefaultConnectionPoolConfig()
		globalTransport = &http.Transport{
			MaxIdleConns:        config.MaxSize,
			MaxIdleConnsPerHost: config.MaxSize,
			IdleConnTimeout:     config.IdleTimeout,
			DialContext: (&net.Dialer{
				Timeout:   config.DialTimeout,
				KeepAlive: config.KeepAlive,
			}).DialContext,
			DisableKeepAlives: false,
		}
	})
	return globalTransport
}

func GetPooledHTTPClient() *http.Client {
	transport := GetGlobalTransport()
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
}
