package sql

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	JoinTypeInner = "INNER"
	JoinTypeLeft  = "LEFT"
	JoinTypeRight = "RIGHT"

	defaultPage     = 1
	defaultPageSize = 10
)

type Corm interface {
	Where(where ...field.Expr) func(*gorm.DB) *gorm.DB
	Or(exprs ...field.Expr) func(db *gorm.DB) *gorm.DB
	Select(selects ...field.Expr) func(*gorm.DB) *gorm.DB
	Page(page, pageSize int) func(*gorm.DB) *gorm.DB
	Order(orders ...field.Expr) func(*gorm.DB) *gorm.DB
	Join(table string, field field.Expr) func(*gorm.DB) *gorm.DB
	LeftJoin(table string, field field.Expr) func(*gorm.DB) *gorm.DB
	RightJoin(table string, field field.Expr) func(*gorm.DB) *gorm.DB
	Distinct(distinct ...field.Expr) func(*gorm.DB) *gorm.DB
	Group(group ...field.Expr) func(*gorm.DB) *gorm.DB
	Having(having ...any) func(*gorm.DB) *gorm.DB
}

type BaseOpr struct {
	db *gorm.DB
}

func (b *BaseOpr) DB(ctx context.Context) *gorm.DB {
	val := ctx.Value(dbKey)
	if val == nil {
		if b.db != nil {
			return b.db
		}
		if defaultDB != nil {
			return defaultDB()
		}
		return nil
	}
	return val.(*gorm.DB)
}

func (b *BaseOpr) SilentDB(ctx context.Context) *gorm.DB {
	db := b.DB(ctx)
	db = db.Session(&gorm.Session{Logger: SilentLogger})
	return db
}

func (b *BaseOpr) Opr(ctx context.Context) *BaseOpr {
	return &BaseOpr{
		db: b.DB(ctx),
	}
}

func (b *BaseOpr) NewDB() *gorm.DB {
	if defaultDB != nil {
		return defaultDB()
	}
	return nil
}

func (b *BaseOpr) Where(where ...field.Expr) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		for _, expr := range where {
			db = db.Where(expr)
		}
		return db
	}
}

func (b *BaseOpr) Or(exprs ...field.Expr) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if len(exprs) == 0 {
			return db
		} else if len(exprs) == 1 {
			return db.Where(exprs[0])
		}
		orDB := b.NewDB().Where(exprs[0])
		for _, expr := range exprs[1:] {
			orDB = orDB.Or(expr)
		}
		return db.Where(orDB)
	}
}

func (b *BaseOpr) Page(page, pageSize int) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if page <= 0 {
			page = defaultPage
		}
		if pageSize <= 0 {
			pageSize = defaultPageSize
		}
		return db.Offset((page - 1) * pageSize).Limit(pageSize)
	}
}

func (b *BaseOpr) Select(selects ...field.Expr) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		queries := buildSelect(db.Statement, selects...)
		if len(queries) > 1 {
			return db.Select(queries[0], queries[1:]...)
		} else if len(selects) == 1 {
			return db.Select(queries[0])
		} else {
			return db
		}
	}
}

func buildSelect(stmt *gorm.Statement, exprs ...field.Expr) (query []any) {
	if len(exprs) == 0 {
		return nil
	}

	var queryItems []any
	for _, e := range exprs {
		sql, _ := e.BuildWithArgs(stmt)
		queryItems = append(queryItems, sql.String())
	}
	return queryItems
}

func (b *BaseOpr) Join(table string, field field.Expr) func(db *gorm.DB) *gorm.DB {
	return b.joins(JoinTypeInner, table, field)
}

func (b *BaseOpr) LeftJoin(table string, field field.Expr) func(db *gorm.DB) *gorm.DB {
	return b.joins(JoinTypeLeft, table, field)
}

func (b *BaseOpr) RightJoin(table string, field field.Expr) func(db *gorm.DB) *gorm.DB {
	return b.joins(JoinTypeRight, table, field)
}

func (b *BaseOpr) joins(joinType string, table string, field field.Expr) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		ret := field.RawExpr()
		fd := ret.(clause.Expr)
		if len(fd.Vars) != 2 {
			return db
		}

		cols := make([]string, 0, 2)

		for _, vr := range fd.Vars {
			v, ok := vr.(clause.Column)
			if !ok {
				return db
			}
			table := v.Table
			if len(v.Alias) > 0 {
				table = v.Alias
			}
			cols = append(cols, fmt.Sprintf("`%s`.`%s`", table, v.Name))
		}
		onCond := strings.Join(cols, " = ")
		return db.Joins(fmt.Sprintf("%s JOIN %s ON %s", joinType, table, onCond))
	}
}

func (b *BaseOpr) Order(orders ...field.Expr) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Order(buildOrder(db, orders...))
	}
}

func buildOrder(db *gorm.DB, exprs ...field.Expr) string {
	stmt, sqlString := buildSQLString(db, exprs...)
	return db.Dialector.Explain(sqlString, stmt.Vars...)
}

func buildSQLString(db *gorm.DB, exprs ...field.Expr) (*gorm.Statement, string) {
	stmt := &gorm.Statement{DB: db.Statement.DB, Table: db.Statement.Table, Schema: db.Statement.Schema}
	for i, c := range exprs {
		if i != 0 {
			stmt.WriteByte(',')
		}
		c.Build(stmt)
	}
	return stmt, stmt.SQL.String()
}

func (b *BaseOpr) Distinct(distinct ...field.Expr) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Distinct(toColExprFullName(db.Statement, distinct...)...)
	}
}

func toColExprFullName(stmt *gorm.Statement, columns ...field.Expr) []any {
	return buildColExpr(stmt, columns, field.WithAll)
}

func buildColExpr(stmt *gorm.Statement, cols []field.Expr, opts ...field.BuildOpt) []any {
	results := make([]any, len(cols))
	for i, c := range cols {
		switch c.RawExpr().(type) {
		case clause.Column:
			results[i] = c.BuildColumn(stmt, opts...).String()
		case clause.Expression:
			sql, args := c.BuildWithArgs(stmt)
			results[i] = stmt.Dialector.Explain(sql.String(), args...)
		}
	}
	return results
}

func (b *BaseOpr) Group(group ...field.Expr) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Group(buildGroup(db, group...))
	}
}

func buildGroup(db *gorm.DB, exprs ...field.Expr) string {
	_, sqlString := buildSQLString(db, exprs...)
	return sqlString
}

func (b *BaseOpr) Having(having ...any) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Having(having[0], having[1:]...)
	}
}
