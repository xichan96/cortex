package sqlite

import (
	"context"
	"database/sql"
	"time"
)

// Phase 2 全局合并的全局锁原语（评审 R2）。
//
// SQLite 不支持 PG 语义的 `ON CONFLICT DO UPDATE WHERE <cond>` 对 conflict
// target 的整体过滤，但支持 `ON CONFLICT(id) DO UPDATE SET ... WHERE <expr>`
// ——该 WHERE 是 UPDATE 的过滤条件。这里用 SQLite 方言实现。
//
// 租约（lease_until）与成功冷却（cooldown_until）拆成两列：
//   - lease_until：认领后最长持有时间（短租约，正常流程会在结束时清 holder）。
//   - cooldown_until：一次成功合并后 6h 内不再跑（对照 codex PHASE2_SUCCESS_COOLDOWN）。
//
// 一个慢的 Phase 2 只会占住短租约，不会因为冷却而阻塞其它实例 6h。

const (
	Phase2LockID     = 1
	Phase2Lease      = 10 * time.Minute // 租约：认领后最长持有时间
	Phase2Cooldown   = 6 * time.Hour    // 成功冷却：成功后 6h 内不再跑
	phase2LockTable  = "memory_phase2_lock"
)

// TryClaimPhase2 以 SQLite 方言认领全局锁。仅当「无持有者 / 租约过期 / 冷却已过」
// 时可抢到，返回是否认领成功。
func TryClaimPhase2(ctx context.Context, db *sql.DB, holder string, lease, cooldown time.Duration) (bool, error) {
	now := time.Now()
	// 可接管条件：租约已释放/过期（lease_until IS NULL OR 过期）AND 冷却未激活
	// （cooldown_until IS NULL OR 已过）。两者都必须满足——被持有中的锁不能被
	// 冷确为 NULL 的语义误抢。
	res, err := db.ExecContext(ctx, `
		INSERT INTO memory_phase2_lock (id, holder, lease_until, cooldown_until, updated_at)
		VALUES (?, ?, ?, NULL, ?)
		ON CONFLICT(id) DO UPDATE SET
			holder = excluded.holder,
			lease_until = excluded.lease_until,
			updated_at = excluded.updated_at
		WHERE (lease_until IS NULL OR lease_until < ?)
		  AND (cooldown_until IS NULL OR cooldown_until < ?)`,
		Phase2LockID, holder, now.Add(lease), now,
		now, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ReleasePhase2 释放全局锁（仅当 holder 匹配时）。
func ReleasePhase2(ctx context.Context, db *sql.DB, holder string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE memory_phase2_lock SET holder = NULL, lease_until = NULL, updated_at = ? WHERE id = ? AND holder = ?`,
		time.Now(), Phase2LockID, holder)
	return err
}

// MarkPhase2Cooldown 记录一次成功合并，其它实例在 cooldown_until 前不再抢。
func MarkPhase2Cooldown(ctx context.Context, db *sql.DB, holder string, cooldown time.Duration) error {
	_, err := db.ExecContext(ctx,
		`UPDATE memory_phase2_lock SET cooldown_until = ? WHERE id = ? AND holder = ?`,
		time.Now().Add(cooldown), Phase2LockID, holder)
	return err
}

// PruneUnused 删除 max_unused_days 内未被引用且无使用计数的 knowledge 条目，
// 以及超期的 preferences。
// 对照 codex max_unused_days：usage_count > 0 的条目永不自动删除（被引用即豁免）。
func PruneUnused(ctx context.Context, db *sql.DB, maxUnusedDays int) error {
	cutoff := time.Now().Add(-time.Duration(maxUnusedDays) * 24 * time.Hour)
	if _, err := db.ExecContext(ctx,
		`DELETE FROM knowledge
		 WHERE COALESCE(usage_count, 0) = 0
		   AND last_usage IS NULL
		   AND updated_at < ?`,
		cutoff); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM preferences
		 WHERE updated_at < ?`,
		cutoff); err != nil {
		return err
	}
	return nil
}
