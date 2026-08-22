package gateway

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（无 CGO）
)

// errResultsStoreUnavailable 在 store 未打开（mkdir/open/init 失败）时由写路径返回，
// 避免调用方把「未落盘」误当成成功。
var errResultsStoreUnavailable = errors.New("results store unavailable")

// resultsStore 任务明细/汇总/统计的 SQLite 持久化（<resultsDir>/results.db），
// 替代旧的 results/<task_id>.json、<task_id>.meta.json、metrics.json 文件。
// 打开时自动把旧 JSON 文件导入并删除（导入成功才删，失败保留待下次重试）。
//
// 表结构：
//   items(task_id, item_id, data JSON, updated_at)  单条明细，主键 (task_id,item_id)
//   task_meta(task_id, data JSON)                   任务收口汇总
//   metrics(key, data JSON)                         统计聚合（单行 key='state'）

type resultsStore struct {
	db *sql.DB
}

// itemRow 是带库内元信息的明细行（DeviceItems 排序用）。
type itemRow struct {
	TaskID    string
	ItemID    string
	Record    itemRecord
	UpdatedAt time.Time
}

func openResultsStore(resultsDir string) *resultsStore {
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		slog.Error("results db mkdir failed", "dir", resultsDir, "error", err)
		return nil
	}
	db, err := sql.Open("sqlite", filepath.Join(resultsDir, "results.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		slog.Error("results db open failed", "error", err)
		return nil
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS items (
	task_id    TEXT NOT NULL,
	item_id    TEXT NOT NULL,
	data       TEXT NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (task_id, item_id)
);
CREATE INDEX IF NOT EXISTS idx_items_updated ON items(updated_at);
CREATE TABLE IF NOT EXISTS task_meta (
	task_id TEXT PRIMARY KEY,
	data    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS metrics (
	key  TEXT PRIMARY KEY,
	data TEXT NOT NULL
);`); err != nil {
		slog.Error("results db init failed", "error", err)
		_ = db.Close()
		return nil
	}
	s := &resultsStore{db: db}
	s.migrateLegacyFiles(resultsDir)
	return s
}

// migrateLegacyFiles 旧 JSON 文件一次性导入：明细、meta、metrics 全部入库后删除文件。
func (s *resultsStore) migrateLegacyFiles(resultsDir string) {
	entries, err := os.ReadDir(resultsDir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		path := filepath.Join(resultsDir, ent.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		mtime := time.Now()
		if info, err := ent.Info(); err == nil {
			mtime = info.ModTime()
		}
		switch {
		case ent.Name() == "metrics.json":
			var f metricsFileState
			if json.Unmarshal(b, &f) == nil && s.saveMetricsState(f) == nil {
				_ = os.Remove(path)
			}
		case strings.HasSuffix(ent.Name(), ".meta.json"):
			var sum TaskSummary
			if json.Unmarshal(b, &sum) == nil && sum.TaskID != "" && s.putMeta(sum) == nil {
				_ = os.Remove(path)
			}
		default:
			taskID := strings.TrimSuffix(ent.Name(), ".json")
			var m map[string]itemRecord
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			ok := true
			for itemID, r := range m {
				if s.putItem(taskID, itemID, r, mtime) != nil {
					ok = false
					break
				}
			}
			if ok {
				_ = os.Remove(path)
			}
		}
	}
	// 清理迁移中间残留的 tmp 文件
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".tmp") {
			_ = os.Remove(filepath.Join(resultsDir, ent.Name()))
		}
	}
}

func (s *resultsStore) itemPersisted(taskID, itemID string) bool {
	if s == nil {
		return false
	}
	var one int
	return s.db.QueryRow(`SELECT 1 FROM items WHERE task_id=? AND item_id=?`, taskID, itemID).Scan(&one) == nil
}

func (s *resultsStore) putItem(taskID, itemID string, r itemRecord, at time.Time) error {
	if s == nil {
		return errResultsStoreUnavailable
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO items(task_id,item_id,data,updated_at) VALUES(?,?,?,?)
ON CONFLICT(task_id,item_id) DO UPDATE SET data=excluded.data, updated_at=excluded.updated_at`,
		taskID, itemID, string(b), at.Unix())
	return err
}

func (s *resultsStore) items(taskID string) map[string]itemRecord {
	m := map[string]itemRecord{}
	if s == nil {
		return m
	}
	rows, err := s.db.Query(`SELECT item_id, data FROM items WHERE task_id=?`, taskID)
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var id, data string
		if rows.Scan(&id, &data) != nil {
			continue
		}
		var r itemRecord
		if json.Unmarshal([]byte(data), &r) == nil {
			m[id] = r
		}
	}
	return m
}

// taskStats 返回某任务明细计数（sent/failed/cancelled/total）。
func (s *resultsStore) taskStats(taskID string) (ok, fail, cancel, total int) {
	if s == nil {
		return 0, 0, 0, 0
	}
	rows, err := s.db.Query(`SELECT json_extract(data,'$.status'), count(*) FROM items WHERE task_id=? GROUP BY 1`, taskID)
	if err != nil {
		return 0, 0, 0, 0
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if rows.Scan(&status, &n) != nil {
			continue
		}
		total += n
		switch status {
		case "sent":
			ok += n
		case "failed":
			fail += n
		case "cancelled":
			cancel += n
		}
	}
	return ok, fail, cancel, total
}

// taskIDsByUpdate 按 updatedAt 倒序返回最近 limit 个任务（含收口时间）。
func (s *resultsStore) taskIDsByUpdate(limit int) []struct {
	TaskID    string
	UpdatedAt int64
} {
	var out []struct {
		TaskID    string
		UpdatedAt int64
	}
	if s == nil {
		return out
	}
	q := `SELECT task_id, MAX(updated_at) AS t FROM items GROUP BY task_id ORDER BY t DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var t int64
		if rows.Scan(&id, &t) == nil {
			out = append(out, struct {
				TaskID    string
				UpdatedAt int64
			}{id, t})
		}
	}
	return out
}

// recentItems 全表按 updated_at 倒序（DeviceItems 视图；内存再做 SentAt 精排）。
func (s *resultsStore) recentItems(limit int) []itemRow {
	var out []itemRow
	if s == nil {
		return out
	}
	q := `SELECT task_id, item_id, data, updated_at FROM items ORDER BY updated_at DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var taskID, itemID, data string
		var ts int64
		if rows.Scan(&taskID, &itemID, &data, &ts) != nil {
			continue
		}
		var r itemRecord
		if json.Unmarshal([]byte(data), &r) == nil {
			out = append(out, itemRow{TaskID: taskID, ItemID: itemID, Record: r, UpdatedAt: time.Unix(ts, 0)})
		}
	}
	return out
}

func (s *resultsStore) putMeta(sum TaskSummary) error {
	if s == nil {
		return errResultsStoreUnavailable
	}
	b, err := json.Marshal(sum)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO task_meta(task_id,data) VALUES(?,?)
ON CONFLICT(task_id) DO UPDATE SET data=excluded.data`, sum.TaskID, string(b))
	return err
}

func (s *resultsStore) meta(taskID string) *TaskSummary {
	if s == nil {
		return nil
	}
	var data string
	if s.db.QueryRow(`SELECT data FROM task_meta WHERE task_id=?`, taskID).Scan(&data) != nil {
		return nil
	}
	var sum TaskSummary
	if json.Unmarshal([]byte(data), &sum) != nil || sum.TaskID == "" {
		return nil
	}
	return &sum
}

func (s *resultsStore) loadMetricsState() (metricsFileState, bool) {
	var f metricsFileState
	if s == nil {
		return f, false
	}
	var data string
	if s.db.QueryRow(`SELECT data FROM metrics WHERE key='state'`).Scan(&data) != nil {
		return f, false
	}
	if json.Unmarshal([]byte(data), &f) != nil {
		return f, false
	}
	return f, true
}

func (s *resultsStore) saveMetricsState(f metricsFileState) error {
	if s == nil {
		return nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO metrics(key,data) VALUES('state',?)
ON CONFLICT(key) DO UPDATE SET data=excluded.data`, string(b))
	return err
}
