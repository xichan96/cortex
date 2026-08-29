package sqlite

import (
	"context"
	"database/sql"
)

// P3.2 user 全局合并：把「以 sessionID 为 user_id 的旧 per-session 条目」归拢到
// 该 session 的归属 user（metadata.user_id）。设计 §4.3，评审 B1/B2 修法。
//
// 评审 B1：迁移后同 user 的内容级重复由随后的 DedupUserKnowledge 真收敛
// （删除重复行），不依赖 Add 的「只合并 tags」副作用。
// 评审 B2：preferences 有 UNIQUE(user_id, category, key)，裸 UPDATE 会撞约束。
// 这里逐条按「updated_at 较新者胜」归并，后写覆盖 + 保留较新的 created_at 语义。

// MigrateLegacySessionKnowledge 迁移「user_id 是 sessionID 的旧条目」到归属 user。
//
// 幂等：已归拢（该 uid 不再是 knowledge/preferences 的 user_id 候选）的 session
// 不重复处理；重复执行不产生重复行。返回处理的条数（knowledge + preferences）。
func MigrateLegacySessionKnowledge(ctx context.Context, db *sql.DB) (int, error) {
	// 候选 uid = 所有可能「是 sessionID」的 user_id（knowledge ∪ preferences）。
	candidates, err := legacySessionUserIDs(ctx, db)
	if err != nil {
		return 0, err
	}

	total := 0
	for _, uid := range candidates {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		owner, ok := sessionOwner(ctx, db, uid)
		if !ok || owner == uid {
			// 无归属（未重建/未被 SetSessionUser 写过的 session）或归属即自身：
			// 保持原样，等它被创建归属后下次迁移处理（设计 §4.2 兼容矩阵）。
			continue
		}
		n, err := migrateSessionRows(ctx, db, uid, owner)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// legacySessionUserIDs 收集 knowledge/preferences 中所有不同的 user_id。
func legacySessionUserIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT user_id FROM knowledge
		 UNION
		 SELECT DISTINCT user_id FROM preferences
		 ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			continue
		}
		if u != "" {
			ids = append(ids, u)
		}
	}
	return ids, rows.Err()
}

// sessionOwner 查一个 session 的归属 user（metadata 'user_id' 键）。
func sessionOwner(ctx context.Context, db *sql.DB, sessionID string) (string, bool) {
	var owner string
	err := db.QueryRowContext(ctx,
		`SELECT value FROM metadata WHERE session_id = ? AND key = 'user_id'`, sessionID).Scan(&owner)
	if err != nil || owner == "" {
		return "", false
	}
	return owner, true
}

// migrateSessionRows 把一个 session 名下的旧条目归拢到 owner。
// knowledge：直接 UPDATE user_id；preferences：逐条按 (owner, cat, key) 冲突消解。
func migrateSessionRows(ctx context.Context, db *sql.DB, uid, owner string) (int, error) {
	n := 0

	// knowledge：无 UNIQUE(user_id, content) 约束，直接改归属；重复留给
	// DedupUserKnowledge 收敛。
	res, err := db.ExecContext(ctx,
		`UPDATE knowledge SET user_id = ? WHERE user_id = ?`, owner, uid)
	if err != nil {
		return n, err
	}
	if c, err := res.RowsAffected(); err == nil {
		n += int(c)
	}

	// preferences：UNIQUE(user_id, category, key)。逐条处理：
	//   - owner 名下无 (cat,key) → 直接 UPDATE 归属；
	//   - owner 名下已有 (cat,key) → 保留 updated_at 较新的一条（后写覆盖），
	//     较旧的一条删除。两条同 updated_at 时保 owner 原条（先写者胜）。
	rows, err := db.QueryContext(ctx,
		`SELECT id, category, key, value, priority, metadata, created_at, updated_at
		 FROM preferences WHERE user_id = ?`, uid)
	if err != nil {
		return n, err
	}
	type prow struct {
		id, cat, key, value, metadata string
		priority                      int
		createdAt, updatedAt          sql.NullTime
	}
	var prefs []prow
	for rows.Next() {
		var p prow
		if err := rows.Scan(&p.id, &p.cat, &p.key, &p.value, &p.priority, &p.metadata, &p.createdAt, &p.updatedAt); err != nil {
			rows.Close()
			return n, err
		}
		prefs = append(prefs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return n, err
	}

	for _, p := range prefs {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		var existingID string
		var existingUpdatedAt sql.NullTime
		err := db.QueryRowContext(ctx,
			`SELECT id, updated_at FROM preferences WHERE user_id = ? AND category = ? AND key = ?`,
			owner, p.cat, p.key).Scan(&existingID, &existingUpdatedAt)
		if err == sql.ErrNoRows {
			// 无冲突：直接改归属。
			if _, err := db.ExecContext(ctx,
				`UPDATE preferences SET user_id = ? WHERE id = ?`, owner, p.id); err != nil {
				return n, err
			}
			n++
			continue
		}
		if err != nil {
			return n, err
		}
		// 有冲突：较新者胜。迁移条更新于 existing → 迁移条接管；
		// 否则删除迁移条（保持 owner 原条）。
		// 顺序注意：必须先 DELETE existing 再 UPDATE 迁移条——两者同属
		// (owner, cat, key)，先 UPDATE 迁移条会撞 UNIQUE(user_id, category, key)。
		replace := newerThan(p.updatedAt, existingUpdatedAt)
		if replace {
			if _, err := db.ExecContext(ctx,
				`DELETE FROM preferences WHERE id = ?`, existingID); err != nil {
				return n, err
			}
			if _, err := db.ExecContext(ctx,
				`UPDATE preferences SET user_id = ? WHERE id = ?`, owner, p.id); err != nil {
				return n, err
			}
			n++
		} else {
			if _, err := db.ExecContext(ctx,
				`DELETE FROM preferences WHERE id = ?`, p.id); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// newerThan 判断 a 是否严格新于 b；任一时间缺失时按「有值者新」处理。
func newerThan(a, b sql.NullTime) bool {
	switch {
	case a.Valid && b.Valid && !a.Time.Equal(b.Time):
		return a.Time.After(b.Time)
	case a.Valid && !b.Valid:
		return true
	case !a.Valid && b.Valid:
		return false
	}
	return false
}
