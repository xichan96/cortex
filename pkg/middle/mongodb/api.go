// Package mongodb
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/qiniu/qmgo"
	cerrors "github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var defaultLogger = logger.NewLogger()

const (
	updateLog = "collection %s, match %d, modify %d, upsert %d"
	deleteLog = "collection %s, delete %d"
	bulkLog   = "mongodb insert %d, match %d, modify %d, upsert %d"
)

// FindAll 查询所有
func (c *Client) FindAll(ctx context.Context, filters bson.M, queryResult interface{}) error {
	err := c.Coll.Find(ctx, filters).All(queryResult)
	return WrapErr(err)
}

// FindOne 查询单个
func (c *Client) FindOne(ctx context.Context, filters bson.M, queryResult interface{}) error {
	err := c.Coll.Find(ctx, filters).One(queryResult)
	return WrapErr(err)
}

// InsertOne 单个插入
func (c *Client) InsertOne(ctx context.Context, data interface{}) (id string, err error) {
	res, err := c.Coll.InsertOne(ctx, data)
	objectID, _ := res.InsertedID.(primitive.ObjectID)
	return objectID.Hex(), WrapErr(err)
}

// Insert 插入数据
func (c *Client) Insert(ctx context.Context, data []interface{}) error {
	_, err := c.Coll.InsertMany(ctx, data)
	return WrapErr(err)
}

// QueryByPaging 分页查询
func (c *Client) QueryByPaging(ctx context.Context, filters bson.M, sort []string, pageIndex int64, pageSize int64, queryResult interface{}) (totalCount int64, err error) {
	search := c.Coll.Find(ctx, filters)
	if sort != nil {
		search = search.Sort(sort...)
	}
	totalCount, _ = search.Count()
	skip := (pageIndex - 1) * pageSize
	err = search.Limit(pageSize).Skip(skip).All(queryResult)
	return totalCount, WrapErr(err)
}

// Update 更新数据
func (c *Client) Update(ctx context.Context, filters bson.M, data bson.M) error {
	err := c.Coll.UpdateOne(ctx, filters, bson.M{"$set": data})
	return WrapErr(err)
}

// UpdateMany 批量更新
func (c *Client) UpdateMany(ctx context.Context, filters bson.M, data bson.M) error {
	res, err := c.Coll.UpdateAll(ctx, filters, bson.M{"$set": data})
	if res != nil {
		defaultLogger.Info(fmt.Sprintf(updateLog, c.Coll.GetCollectionName(), res.MatchedCount, res.ModifiedCount, res.UpsertedCount),
			slog.String("collection", c.Coll.GetCollectionName()),
			slog.Int64("matched", res.MatchedCount),
			slog.Int64("modified", res.ModifiedCount),
			slog.Int64("upserted", res.UpsertedCount))
	}
	return WrapErr(err)
}

// DeleteAll 删除所有符合条件的数据
func (c *Client) DeleteAll(ctx context.Context, data bson.M) error {
	res, err := c.Coll.RemoveAll(ctx, data)
	if res != nil {
		defaultLogger.Info(fmt.Sprintf(deleteLog, c.Coll.GetCollectionName(), res.DeletedCount),
			slog.String("collection", c.Coll.GetCollectionName()),
			slog.Int64("deleted", res.DeletedCount))
	}
	return WrapErr(err)
}

// GetBulkContainer 获取批量执行容器
func (c *Client) GetBulkContainer(collection string) (bulk *qmgo.Bulk) {
	return c.Coll.Bulk()
}

// BulkExecute 批量执行
func (c *Client) BulkExecute(ctx context.Context, bulk *qmgo.Bulk) (err error) {
	res, err := bulk.Run(ctx)
	if res != nil {
		defaultLogger.Info(fmt.Sprintf(bulkLog, res.InsertedCount, res.MatchedCount, res.ModifiedCount, res.UpsertedCount),
			slog.Int64("inserted", res.InsertedCount),
			slog.Int64("matched", res.MatchedCount),
			slog.Int64("modified", res.ModifiedCount),
			slog.Int64("upserted", res.UpsertedCount))
	}
	return WrapErr(err)
}

// IsNoFoundError 判断不存在
func IsNoFoundError(err error) bool {
	return errors.Is(err, qmgo.ErrNoSuchDocuments)
}

// WrapErr 包装mongodb的错误
func WrapErr(err error) error {
	if err == nil {
		return nil
	}
	if IsNoFoundError(err) {
		return cerrors.EC_DATA_NOT_FOUND
	}
	return cerrors.NewError(cerrors.EC_INTERNAL_ERROR.Code, "mongodb error").Wrap(err)
}
