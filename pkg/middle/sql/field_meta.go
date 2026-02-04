package sql

import (
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"

	"github.com/xichan96/cortex/pkg/logger"
	"gorm.io/gen/field"
	"gorm.io/gorm/schema"
)

var fieldMetaMapping = sync.Map{}

func NewCTable(tableName string, ass ...string) CTable {
	var as string
	if len(ass) > 0 {
		as = ass[0]
	}
	return cTable{
		_TableName: tableName,
		_As:        as,
	}
}

type cTable struct {
	_TableName string
	_As        string
}

func (c cTable) TableName() string {
	return c._TableName
}

func (c cTable) As() string {
	return c._As
}

func (c cTable) Table() string {
	if len(c.As()) > 0 {
		return fmt.Sprintf("%s AS %s", c.TableName(), c.As())
	}
	return c.TableName()
}

type CTable interface {
	TableName() string
	Table() string
	As() string
}

func NewGlobalFieldMetaMapping[T CTable](ml schema.Tabler, fm T, tAs ...string) *T {
	cField := reflect.ValueOf(&fm).Elem().FieldByName("CTable")
	cVal := reflect.ValueOf(NewCTable(ml.TableName(), tAs...))
	cField.Set(cVal)

	val, ok := fieldMetaMapping.Load(ml.TableName())
	if ok {
		return val.(*T)
	}
	logger.NewLogger().Info("NewGlobalFieldMetaMapping: %s", slog.String("table_name", ml.TableName()))
	fmp := NewFieldMetaMapping(ml, fm)
	fieldMetaMapping.Store(ml.TableName(), fmp)
	return fmp
}

func NewFieldMetaMapping[T CTable](ml schema.Tabler, fm T) *T {
	tableName := fm.TableName()
	if len(fm.As()) > 0 {
		tableName = fm.As()
	}

	v := reflect.ValueOf(&fm).Elem()
	t := reflect.TypeOf(ml)

	for i := 0; i < t.NumField(); i++ {
		modelField := t.Field(i)

		columnName := getColumnName(modelField)
		if len(columnName) == 0 {
			continue
		}

		persistField := v.FieldByName(modelField.Name)
		if !persistField.IsValid() || !persistField.CanSet() {
			continue
		}

		kind := modelField.Type.Kind()
		if kind == reflect.Ptr {
			kind = modelField.Type.Elem().Kind()
		}

		switch kind {
		case reflect.String:
			persistField.Set(reflect.ValueOf(field.NewString(tableName, columnName)))
		case reflect.Bool:
			persistField.Set(reflect.ValueOf(field.NewBool(tableName, columnName)))
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
			persistField.Set(reflect.ValueOf(field.NewInt(tableName, columnName)))
		case reflect.Int64:
			persistField.Set(reflect.ValueOf(field.NewInt64(tableName, columnName)))
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
			persistField.Set(reflect.ValueOf(field.NewUint(tableName, columnName)))
		case reflect.Uint64:
			persistField.Set(reflect.ValueOf(field.NewUint64(tableName, columnName)))
		case reflect.Float32, reflect.Float64:
			persistField.Set(reflect.ValueOf(field.NewFloat64(tableName, columnName)))
		case reflect.Struct:
			if modelField.Type.String() == "time.Time" {
				persistField.Set(reflect.ValueOf(field.NewTime(tableName, columnName)))
			} else {
				persistField.Set(reflect.ValueOf(field.NewField(tableName, columnName)))
			}
		default:
			persistField.Set(reflect.ValueOf(field.NewField(tableName, columnName)))
		}
	}
	return &fm
}

func getColumnName(sf reflect.StructField) string {
	gormTag := sf.Tag.Get("gorm")
	jsonTag := sf.Tag.Get("json")

	if len(gormTag) == 0 && len(jsonTag) == 0 {
		return ""
	}

	columnName := parseGormTag(gormTag)
	if len(columnName) > 0 {
		return columnName
	}

	columnName = parseJSONTag(jsonTag)
	if len(columnName) == 0 {
		return ""
	}

	return columnName
}

func parseGormTag(tag string) string {
	parts := strings.Split(tag, ";")

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "column:") {
			kv := strings.SplitN(part, ":", 2)
			if len(kv) == 2 {
				return strings.TrimSpace(kv[1])
			}
		}
	}

	return ""
}

func parseJSONTag(tag string) string {
	parts := strings.Split(tag, ",")

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len(part) > 0 && part != "omitempty" {
			return part
		}
	}

	return ""
}
