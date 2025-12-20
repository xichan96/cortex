package cache

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gitlab.gz.cvte.cn/research_engineer/kit/ec"
)

// LocalCacheIer local cache interface
type LocalCacheIer interface {
	IsExists(key string) bool
	Set(key string, data interface{}, expire time.Duration) error
	Get(key string, result interface{}) error
	Del(key string) error
	Close()
}

type Value struct {
	Data   any
	Expire int64
}

type LocalCache struct {
	db     map[string]Value
	lock   sync.RWMutex
	cancel context.CancelFunc
}

func NewLocalCache() LocalCacheIer {
	ctx, cancel := context.WithCancel(context.Background())
	ca := &LocalCache{
		db:     make(map[string]Value),
		cancel: cancel,
	}
	go ca.background(ctx)
	return ca
}

func (c *LocalCache) Close() {
	c.cancel()
}

func (c *LocalCache) Set(key string, data interface{}, expire time.Duration) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	var expireTime int64
	if expire > 0 {
		expireTime = time.Now().Add(expire).Unix()
	} else {
		expireTime = 0
	}
	c.db[key] = Value{
		Data:   data,
		Expire: expireTime,
	}
	return nil
}

// Get get cache data
func (c *LocalCache) Get(key string, result interface{}) error {
	c.lock.RLock()
	defer c.lock.RUnlock()
	data, ok := c.db[key]
	if !ok {
		return ec.NoFound
	}
	// check if data is expired
	if data.Expire > 0 && time.Now().Unix() >= data.Expire {
		return ec.NoFound
	}
	// copy data to result using reflection
	if result == nil {
		return fmt.Errorf("result cannot be nil")
	}
	val := reflect.ValueOf(result)
	if val.Kind() != reflect.Ptr {
		return fmt.Errorf("result must be a pointer")
	}
	if val.IsNil() {
		return fmt.Errorf("result pointer is nil")
	}
	elem := val.Elem()
	dataVal := reflect.ValueOf(data.Data)
	if !dataVal.IsValid() {
		return fmt.Errorf("cached data is invalid")
	}
	if !dataVal.Type().AssignableTo(elem.Type()) {
		return fmt.Errorf("cannot assign %s to %s", dataVal.Type(), elem.Type())
	}
	elem.Set(dataVal)
	return nil
}

// GetValue get cache data
func (c *LocalCache) GetValue(key string) (value any, err error) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	data, ok := c.db[key]
	if !ok {
		return value, ec.NoFound
	}
	// check if data is expired
	if data.Expire > 0 && time.Now().Unix() >= data.Expire {
		return value, ec.NoFound
	}
	return data.Data, nil
}

func (c *LocalCache) Del(key string) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	_, ok := c.db[key]
	if !ok {
		return ec.NoFound
	}
	delete(c.db, key)
	return nil
}

func (c *LocalCache) TTL(key string) (expire int64, err error) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	data, ok := c.db[key]
	if !ok {
		return 0, ec.NoFound
	}
	return data.Expire, nil
}

func (c *LocalCache) IsExists(key string) bool {
	c.lock.RLock()
	defer c.lock.RUnlock()
	_, ok := c.db[key]
	return ok
}

func (c *LocalCache) background(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Msgf("panic in background: %v", r)
		}
	}()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		func() {
			c.lock.Lock()
			defer c.lock.Unlock()
			nowTime := time.Now().Unix()
			for key, val := range c.db {
				if val.Expire == 0 {
					continue
				}
				if nowTime >= val.Expire {
					delete(c.db, key)
				}
			}
		}()
	}
}
