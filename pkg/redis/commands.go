// Package redis 对于redis进行封装
package redis

import (
	"context"
	"encoding/json"
	"time"

	cerrors "github.com/xichan96/cortex/pkg/errors"
)

const (
	batchSize = 1000
)

// ScanKeys 扫描所有符合key
func (c *Client) ScanKeys(ctx context.Context, match string) ([]string, error) {
	var cursor uint64
	ret := make([]string, 0)
	for {
		var keys []string
		var err error
		keys, cursor, err = c.Scan(ctx, cursor, match, batchSize).Result()
		if err != nil {
			return nil, WrapRedisErr(err)
		}
		ret = append(ret, keys...)
		if cursor == 0 {
			break
		}
	}
	return ret, nil
}

// GetObject 反序列化成obj对象
func (c *Client) GetObject(ctx context.Context, key string, obj any) error {
	result, err := c.Get(ctx, key).Result()
	if err != nil {
		return WrapRedisErr(err)
	}

	if err := json.Unmarshal([]byte(result), obj); err != nil {
		return cerrors.NewError(cerrors.EC_DATA_FORMAT_INVALID.Code, "redis unmarshal error").Wrap(err)
	}
	return nil
}

// SetObjectEx 设置一个对象，并且加入过期时间
func (c *Client) SetObjectEx(ctx context.Context, key string, val any, expire time.Duration) error {
	body, err := json.Marshal(val)
	if err != nil {
		return cerrors.NewError(cerrors.EC_DATA_FORMAT_INVALID.Code, "redis marshal error").Wrap(err)
	}
	_, err = c.Set(ctx, key, body, expire).Result()
	return WrapRedisErr(err)
}
