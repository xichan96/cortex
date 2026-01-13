package mysql

import (
	"fmt"
	"time"

	"github.com/xichan96/cortex/pkg/sql"
	gmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

const (
	defaultMaxOpenConn = 10
	defaultMaxIdleConn = 3
	defaultMaxIdleTime = 2 * time.Minute
	mysqlPattern       = "%s:%s@(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&maxAllowedPacket=102400000"
)

type Config struct {
	Host             string         `json:"host"`
	Port             int            `json:"port"`
	User             string         `json:"user"`
	Password         string         `json:"password"`
	Database         string         `json:"database"`
	SlaveHost        string         `json:"slave_host,omitempty"`
	SlavePort        int            `json:"slave_port,omitempty"`
	SlaveUser        string         `json:"slave_user,omitempty"`
	SlavePassword    string         `json:"slave_password,omitempty"`
	SlaveDatabase    string         `json:"slave_database,omitempty"`
	MaxOpenConn      int            `json:"max_open_conn,omitempty"`
	MaxIdleConn      int            `json:"max_idle_conn,omitempty"`
	MaxIdleTimeSec   int            `json:"max_life_time_sec,omitempty"`
	LogConfig        *sql.LogConfig `json:"log_config,omitempty"`
	DisableErrorHook bool           `json:"disable_error_hook,omitempty"`
}

type Client struct {
	DB *gorm.DB
}

func NewClient(cfg *Config) (c *Client, err error) {
	masterURI := fmt.Sprintf(mysqlPattern, cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)
	db, err := gorm.Open(gmysql.Open(masterURI), &gorm.Config{Logger: sql.NewLogger(cfg.LogConfig)})
	if err != nil {
		return nil, err
	}

	if len(cfg.SlaveHost) > 0 && cfg.SlavePort > 0 && len(cfg.SlaveDatabase) > 0 && len(cfg.SlaveUser) > 0 && len(cfg.SlavePassword) > 0 {
		slaveURI := fmt.Sprintf(mysqlPattern, cfg.User, cfg.Password, cfg.SlaveHost, cfg.SlavePort, cfg.Database)
		if err = db.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{gmysql.Open(slaveURI)}})); err != nil {
			return nil, err
		}
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}

	openConn := defaultMaxOpenConn
	idleConn := defaultMaxIdleConn
	idleTime := defaultMaxIdleTime
	if cfg.MaxOpenConn != 0 {
		openConn = cfg.MaxOpenConn
	}
	if cfg.MaxIdleConn != 0 {
		idleConn = cfg.MaxIdleConn
	}
	if cfg.MaxIdleTimeSec != 0 {
		idleTime = time.Duration(cfg.MaxIdleTimeSec) * time.Second
	}
	sqlDB.SetMaxOpenConns(openConn)
	sqlDB.SetMaxIdleConns(idleConn)
	sqlDB.SetConnMaxIdleTime(idleTime)

	if !cfg.DisableErrorHook {
		RegisterCallbacks(db)
	}

	return &Client{DB: db}, nil
}
