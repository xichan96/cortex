package sql

import (
	"context"

	"gorm.io/gorm"

	"github.com/xichan96/cortex/pkg/errors"
)

type Transaction interface {
	Execute(ctx context.Context, fn func(context.Context) error) error
}

type contextKey string

const (
	dbKey contextKey = "__db"
	txKey contextKey = "__tx"
)

var (
	DBKeyError     = errors.EC_SQL_DB_KEY_ERROR
	DefaultDBError = errors.EC_SQL_DEFAULT_DB_ERROR
)

type DefaultDB func() *gorm.DB

var defaultDB DefaultDB

func SetDefaultDB(fn DefaultDB) {
	defaultDB = fn
}

type SqlTX struct{}

func NewSqlTX() Transaction {
	return &SqlTX{}
}

func (m SqlTX) Execute(ctx context.Context, fun func(ctx context.Context) error) error {
	dbVal := ctx.Value(dbKey)
	txVal := ctx.Value(txKey)

	var db *gorm.DB
	if dbVal != nil {
		var ok bool
		db, ok = dbVal.(*gorm.DB)
		if !ok {
			return DBKeyError
		}
	} else {
		if defaultDB == nil {
			return DefaultDBError
		}
		db = defaultDB()
	}

	if txVal != nil {
		return fun(ctx)
	}

	tx := db.Begin()
	defer tx.Rollback()

	newCtx := context.WithValue(ctx, dbKey, tx)
	newCtx = context.WithValue(newCtx, txKey, struct{}{})
	if err := fun(newCtx); err != nil {
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	return nil
}

func NewBaseOpr(db *gorm.DB) *BaseOpr {
	return &BaseOpr{db: db}
}
