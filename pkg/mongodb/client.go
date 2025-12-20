// Package mongodb
package mongodb

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/qiniu/qmgo"
	"github.com/qiniu/qmgo/options"
	cerrors "github.com/xichan96/cortex/pkg/errors"
	opts "go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

const (
	defaultMaxPoolSize       = 100
	defaultMinPoolSize       = 0
	defaultConnTimeout       = 30 * time.Second
	defaultSocketTimeout     = 15 * time.Second
	defaultHeartbeatInterval = 10 * time.Second
	defaultLocalThreshold    = 15 * time.Millisecond
	defaultMaxConnIdleTime   = 120 * time.Second
	defaultRetryRead         = true
	defaultRetryWrite        = true
	defaultDirect            = false
)

// Config mongodb配置
type Config struct {
	URI         string `json:"uri"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Database    string `json:"database"`
	Collection  string `json:"collection"`
	MaxPoolSize uint64 `json:"max_pool_size"`
	MinPoolSize uint64 `json:"min_pool_size"`
	// 是否仅连接提供的主机，false将会发现集群其它主机
	Direct bool `json:"direct"`
	// socket读写超时时间，单位秒
	SocketTimeout time.Duration `json:"socket_timeout"`
	// 建立连接超时时间，单位秒
	ConnTimeout time.Duration `json:"conn_timeout"`
	// 空闲连接最大持续时间，单位秒
	MaxConnIdleTime time.Duration `json:"max_conn_idle_time"`
	// 心跳检查间隔，单位秒
	HeartbeatInterval time.Duration `json:"heartbeatInterval"`
	// 延迟窗口的容忍大小，单位毫秒
	LocalThreshold time.Duration `json:"local_threshold"`
	// 重复读，默认开启
	RetryRead bool `json:"retry_read"`
	// 重复写，默认开启
	RetryWrite bool `json:"retry_write"`

	// 读取关注
	readConcern *readconcern.ReadConcern
	// 写入关注
	writeConcern *writeconcern.WriteConcern
	// tls / ssl 参数
	tlsConfig *tls.Config
}

// Client 客户端
type Client struct {
	*qmgo.Client

	Config *Config
	DB     *qmgo.Database
	Coll   *qmgo.Collection
}

type ClientOptionFunc func(c *Client)

// NewClient is 初始化mongodb客户端
func NewClient(fn ...ClientOptionFunc) (c *Client, err error) {
	c = &Client{
		Config: &Config{
			MaxPoolSize:       defaultMaxPoolSize,
			MinPoolSize:       defaultMinPoolSize,
			Direct:            defaultDirect,
			SocketTimeout:     defaultSocketTimeout,
			ConnTimeout:       defaultConnTimeout,
			MaxConnIdleTime:   defaultMaxConnIdleTime,
			HeartbeatInterval: defaultHeartbeatInterval,
			LocalThreshold:    defaultLocalThreshold,
			RetryRead:         defaultRetryRead,
			RetryWrite:        defaultRetryWrite,
		},
	}

	for _, f := range fn {
		f(c)
	}

	qConfig := &qmgo.Config{
		Uri:      c.Config.URI,
		Database: c.Config.Database,
		Coll:     c.Config.Collection,
	}
	if len(c.Config.Username) > 0 && len(c.Config.Password) > 0 {
		cred := qmgo.Credential{
			Username: c.Config.Username,
			Password: c.Config.Password,
		}
		qConfig.Auth = &cred
	}

	opts := options.ClientOptions{
		ClientOptions: &opts.ClientOptions{
			MaxPoolSize:       &c.Config.MaxPoolSize,
			MinPoolSize:       &c.Config.MinPoolSize,
			Direct:            &c.Config.Direct,
			SocketTimeout:     &c.Config.SocketTimeout,
			ConnectTimeout:    &c.Config.ConnTimeout,
			MaxConnIdleTime:   &c.Config.MaxConnIdleTime,
			HeartbeatInterval: &c.Config.HeartbeatInterval,
			LocalThreshold:    &c.Config.LocalThreshold,
			RetryReads:        &c.Config.RetryRead,
			RetryWrites:       &c.Config.RetryWrite,
		},
	}
	if c.Config.tlsConfig != nil {
		opts.SetTLSConfig(c.Config.tlsConfig)
	}
	if c.Config.readConcern != nil {
		opts.SetReadConcern(c.Config.readConcern)
	}
	if c.Config.writeConcern != nil {
		opts.SetWriteConcern(c.Config.writeConcern)
	}

	client, err := qmgo.Open(context.Background(), qConfig, opts)
	if err != nil {
		return nil, cerrors.NewError(cerrors.EC_CONNECTION_FAILED.Code, "failed to connect to mongodb").Wrap(err)
	}
	c.Client = client.Client
	c.DB = client.Database
	c.Coll = client.Collection
	return c, nil
}

// SetURI is 设置资源地址
func SetURI(uri string) ClientOptionFunc {
	return func(c *Client) {
		if len(uri) > 0 {
			c.Config.URI = uri
		}
	}
}

// SetBasicAuth is 设置账号
func SetBasicAuth(username, password string) ClientOptionFunc {
	return func(c *Client) {
		c.Config.Username = username
		c.Config.Password = password
	}
}

// SetDatabase is 设置db
func SetDatabase(db string) ClientOptionFunc {
	return func(c *Client) {
		c.Config.Database = db
	}
}

// SetCollection is 设置集合
func SetCollection(coll string) ClientOptionFunc {
	return func(c *Client) {
		c.Config.Collection = coll
	}
}

// SetMaxPoolSize is 设置最大连接池数量
func SetMaxPoolSize(size int) ClientOptionFunc {
	return func(c *Client) {
		if size >= 0 {
			c.Config.MaxPoolSize = uint64(size)
		}
	}
}

// SetMinPoolSize is 设置最小连接池数量
func SetMinPoolSize(size int) ClientOptionFunc {
	return func(c *Client) {
		if size >= 0 {
			c.Config.MinPoolSize = uint64(size)
		}
	}
}

// SetDirect is 设置是否仅连接提供的主机
func SetDirect(enabled bool) ClientOptionFunc {
	return func(c *Client) {
		c.Config.Direct = enabled
	}
}

// SetSocketTimeout is 设置读写超时时间
func SetSocketTimeout(t int) ClientOptionFunc {
	return func(c *Client) {
		if t >= 0 {
			c.Config.SocketTimeout = time.Duration(t) * time.Second
		}
	}
}

// SetConnectTimeout is 设置连接超时时间
func SetConnectTimeout(t int) ClientOptionFunc {
	return func(c *Client) {
		if t >= 0 {
			c.Config.ConnTimeout = time.Duration(t) * time.Second
		}
	}
}

// SetMaxConnIdleTime is 设置连接最大空闲时间
func SetMaxConnIdleTime(t int) ClientOptionFunc {
	return func(c *Client) {
		if t >= 0 {
			c.Config.MaxConnIdleTime = time.Duration(t) * time.Second
		}
	}
}

// SetHeartbeatInterval is 设置心跳间隔
func SetHeartbeatInterval(t int) ClientOptionFunc {
	return func(c *Client) {
		if t >= 0 {
			c.Config.HeartbeatInterval = time.Duration(t) * time.Second
		}
	}
}

// SetLocalThreshold is 设置延迟窗口
func SetLocalThreshold(t int) ClientOptionFunc {
	return func(c *Client) {
		if t >= 0 {
			c.Config.LocalThreshold = time.Duration(t) * time.Millisecond
		}
	}
}

// SetRetryRead is 设置是否重复读
func SetRetryRead(enabled bool) ClientOptionFunc {
	return func(c *Client) {
		c.Config.RetryRead = enabled
	}
}

// SetRetryWrite is 设置是否重复写
func SetRetryWrite(enabled bool) ClientOptionFunc {
	return func(c *Client) {
		c.Config.RetryWrite = enabled
	}
}

// SetReadConcern is 设置读关注等级
func SetReadConcern(level string) ClientOptionFunc {
	return func(c *Client) {
		if len(level) > 0 {
			c.Config.readConcern = readconcern.New(readconcern.Level(level))
		}
	}
}

// SetWriteConcern is 设置写关注等级
func SetWriteConcern(level interface{}) ClientOptionFunc {
	return func(c *Client) {
		switch level := level.(type) {
		case int:
			if level == 0 || level == 1 {
				c.Config.writeConcern = writeconcern.New(writeconcern.W(level))
			}
		case string:
			if level == "majority" {
				c.Config.writeConcern = writeconcern.New(writeconcern.WMajority())
			}
		}
	}
}

func (c *Client) clone() *Client {
	clone := *c
	return &clone
}

// Collection 设置集合
func (c *Client) Collection(name string) *Client {
	clone := c.clone()
	clone.Coll = c.DB.Collection(name)
	return clone
}
