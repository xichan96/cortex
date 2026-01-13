package sqlite

import (
	"time"

	"github.com/xichan96/cortex/pkg/sql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	defaultMaxOpenConn = 10
	defaultMaxIdleConn = 3
	defaultMaxIdleTime = 2 * time.Minute
)

type Config struct {
	Path             string         `json:"path"`
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
	db, err := gorm.Open(sqlite.Open(cfg.Path), &gorm.Config{Logger: sql.NewLogger(cfg.LogConfig)})
	if err != nil {
		return nil, err
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
