package mysql

import (
	"errors"

	sql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	cerrors "github.com/xichan96/cortex/pkg/errors"
)

const (
	wrapErrorName = "wrap_error"
)

func RegisterCallbacks(db *gorm.DB) {
	db.Callback().Create().After("gorm:after_create").Register(wrapErrorName, wrapErrCallback)
	db.Callback().Query().After("gorm:after_query").Register(wrapErrorName, wrapErrCallback)
	db.Callback().Delete().After("gorm:after_delete").Register(wrapErrorName, wrapErrCallback)
	db.Callback().Update().After("gorm:after_update").Register(wrapErrorName, wrapErrCallback)
	db.Callback().Row().After("gorm:row").Register(wrapErrorName, wrapErrCallback)
	db.Callback().Raw().After("gorm:raw").Register(wrapErrorName, wrapErrCallback)
}

func wrapErrCallback(db *gorm.DB) {
	if db.Error != nil {
		db.Error = WrapErr(db.Error)
	}
}

func WrapErr(err error) error {
	if err == nil {
		return nil
	}
	if IsNoFoundError(err) {
		return cerrors.EC_SQL_NOT_FOUND
	} else if IsDuplicateKeyError(err) {
		return cerrors.EC_SQL_DUPLICATE_KEY
	}
	return cerrors.WrapWithSkip(4, err)
}

func IsDuplicateKeyError(err error) bool {
	mysqlErr, ok := err.(*sql.MySQLError)
	return ok && mysqlErr.Number == 1062
}

func IsNoFoundError(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func OperatorErr(db *gorm.DB) (rows int64, err error) {
	return db.RowsAffected, WrapErr(err)
}
