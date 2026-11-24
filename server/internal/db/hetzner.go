// Package db 的 Hetzner 部分：快照记录、自动重建策略、流量日统计、qB 节点配置。
package db

import (
	"fmt"
	"time"

	"github.com/ovh-buy/server/internal/secret"
)

// HetznerSnapshot 快照本地记录（API 侧以 Hetzner images 为准）。
type HetznerSnapshot struct {
	ID          int64   `db:"id" json:"id"`
	ServerID    int64   `db:"server_id" json:"server_id"`
	Description string  `db:"description" json:"description"`
	SizeGB      float64 `db:"size_gb" json:"size_gb"`
	CreatedAt   string  `db:"created_at" json:"created"`
	UpdatedAt   string  `db:"updated_at" json:"-"`
}

// HetznerPolicy 自动重建策略：Threshold 为流量占比阈值 (0,1]，
// ImageID 为重建用的镜像/快照（数字 id 或镜像名），空表示未配。
type HetznerPolicy struct {
	ServerID  int64   `db:"server_id" json:"server_id"`
	Enabled   bool    `db:"enabled" json:"enabled"`
	Threshold float64 `db:"threshold" json:"threshold"`
	ImageID   string  `db:"image_id" json:"image_id"`
	UpdatedAt string  `db:"updated_at" json:"-"`
}

// HetznerDailyStat 单台服务器某日的出流量（字节）。
type HetznerDailyStat struct {
	ServerID  int64  `db:"server_id" json:"server_id"`
	Date      string `db:"date" json:"date"`
	Bytes     int64  `db:"bytes" json:"bytes"`
	UpdatedAt string `db:"updated_at" json:"-"`
}

// HetznerQBNode qBittorrent 节点配置。Password 在库里是 secret 加密后的密文，
// 读出时解密；只做状态查询与流量展示，不做任何写入/删种操作。
// Password 永不参与 JSON 序列化（json:"-"），防止配置经接口泄漏。
type HetznerQBNode struct {
	ServerID int64  `db:"server_id" json:"server_id"`
	URL      string `db:"url" json:"url"`
	Username string `db:"username" json:"username"`
	Password string `db:"password" json:"-"`
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

// UpsertHetznerSnapshot 记录一次快照创建（幂等 upsert）。
func (db *DB) UpsertHetznerSnapshot(s HetznerSnapshot) error {
	if s.UpdatedAt == "" {
		s.UpdatedAt = nowISO()
	}
	_, err := db.NamedExec(`
		INSERT INTO hetzner_snapshots (id, server_id, description, size_gb, created_at, updated_at)
		VALUES (:id, :server_id, :description, :size_gb, :created_at, :updated_at)
		ON CONFLICT(id) DO UPDATE SET
		  server_id = excluded.server_id,
		  description = excluded.description,
		  size_gb = excluded.size_gb,
		  created_at = excluded.created_at,
		  updated_at = excluded.updated_at`,
		s)
	if err != nil {
		return fmt.Errorf("upsert hetzner snapshot %d: %w", s.ID, err)
	}
	return nil
}

// ListHetznerSnapshots 列出本地快照记录（serverID=0 时列全部）。
func (db *DB) ListHetznerSnapshots(serverID int64) ([]HetznerSnapshot, error) {
	var rows []HetznerSnapshot
	var err error
	if serverID == 0 {
		err = db.Select(&rows, `SELECT * FROM hetzner_snapshots ORDER BY created_at DESC`)
	} else {
		err = db.Select(&rows, `SELECT * FROM hetzner_snapshots WHERE server_id = ? ORDER BY created_at DESC`, serverID)
	}
	if err != nil {
		return nil, fmt.Errorf("list hetzner snapshots: %w", err)
	}
	if rows == nil {
		rows = []HetznerSnapshot{}
	}
	return rows, nil
}

// DeleteHetznerSnapshot 按 id 删本地快照记录。
func (db *DB) DeleteHetznerSnapshot(id int64) error {
	_, err := db.Exec(`DELETE FROM hetzner_snapshots WHERE id = ?`, id)
	return err
}

// ListHetznerPolicies 列出全部自动重建策略。
func (db *DB) ListHetznerPolicies() ([]HetznerPolicy, error) {
	type row struct {
		ServerID  int64   `db:"server_id"`
		Enabled   int     `db:"enabled"`
		Threshold float64 `db:"threshold"`
		ImageID   string  `db:"image_id"`
		UpdatedAt string  `db:"updated_at"`
	}
	var rows []row
	if err := db.Select(&rows, `SELECT * FROM hetzner_policies ORDER BY server_id`); err != nil {
		return nil, fmt.Errorf("list hetzner policies: %w", err)
	}
	out := make([]HetznerPolicy, 0, len(rows))
	for _, r := range rows {
		out = append(out, HetznerPolicy{ServerID: r.ServerID, Enabled: r.Enabled == 1, Threshold: r.Threshold, ImageID: r.ImageID, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}

// GetHetznerPolicy 取一台服务器的策略，不存在返回 nil, nil。
func (db *DB) GetHetznerPolicy(serverID int64) (*HetznerPolicy, error) {
	policies, err := db.ListHetznerPolicies()
	if err != nil {
		return nil, err
	}
	for i := range policies {
		if policies[i].ServerID == serverID {
			return &policies[i], nil
		}
	}
	return nil, nil
}

// UpsertHetznerPolicy 按 server_id upsert 策略。
func (db *DB) UpsertHetznerPolicy(p HetznerPolicy) error {
	if p.UpdatedAt == "" {
		p.UpdatedAt = nowISO()
	}
	_, err := db.Exec(`
		INSERT INTO hetzner_policies (server_id, enabled, threshold, image_id, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(server_id) DO UPDATE SET
		  enabled = excluded.enabled,
		  threshold = excluded.threshold,
		  image_id = excluded.image_id,
		  updated_at = excluded.updated_at`,
		p.ServerID, boolToInt(p.Enabled), p.Threshold, p.ImageID, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert hetzner policy %d: %w", p.ServerID, err)
	}
	return nil
}

// DeleteHetznerPolicy 按 server_id 删策略。
func (db *DB) DeleteHetznerPolicy(serverID int64) error {
	_, err := db.Exec(`DELETE FROM hetzner_policies WHERE server_id = ?`, serverID)
	return err
}

// UpsertHetznerDailyStat 写入一日流量（同一天多次写入取最大值：
// metrics 是累计口径的近似值，取 max 避免重复累加把同一天算两遍）。
func (db *DB) UpsertHetznerDailyStat(serverID int64, date string, bytes int64) error {
	_, err := db.Exec(`
		INSERT INTO hetzner_daily_stats (server_id, date, bytes, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(server_id, date) DO UPDATE SET
		  bytes = MAX(bytes, excluded.bytes),
		  updated_at = excluded.updated_at`,
		serverID, date, bytes, nowISO())
	if err != nil {
		return fmt.Errorf("upsert hetzner daily stat %d %s: %w", serverID, date, err)
	}
	return nil
}

// AddHetznerDailyStat 把 bytes 累加进某日（用于删除/重建时沉淀历史：
// 机器删了流量清零，历史要留下来；同一天多次沉淀要累加）。
func (db *DB) AddHetznerDailyStat(serverID int64, date string, bytes int64) error {
	_, err := db.Exec(`
		INSERT INTO hetzner_daily_stats (server_id, date, bytes, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(server_id, date) DO UPDATE SET
		  bytes = bytes + excluded.bytes,
		  updated_at = excluded.updated_at`,
		serverID, date, bytes, nowISO())
	if err != nil {
		return fmt.Errorf("add hetzner daily stat %d %s: %w", serverID, date, err)
	}
	return nil
}

// ListHetznerDailyStats 取某台服务器最近 days 天的日统计（serverID=0 时取全部服务器）。
// 返回按 date 升序；archivedID 为 -1 的特殊行不存在——归档行用 server_id=0 表示“已删除/重建累计”。
func (db *DB) ListHetznerDailyStats(serverID int64, days int) ([]HetznerDailyStat, error) {
	var rows []HetznerDailyStat
	var err error
	if days <= 0 {
		days = 30
	}
	if serverID == 0 {
		err = db.Select(&rows, `SELECT * FROM hetzner_daily_stats ORDER BY date ASC LIMIT ?`, days*100)
	} else {
		err = db.Select(&rows, `SELECT * FROM hetzner_daily_stats WHERE server_id = ? ORDER BY date ASC LIMIT ?`, serverID, days)
	}
	if err != nil {
		return nil, fmt.Errorf("list hetzner daily stats: %w", err)
	}
	if rows == nil {
		rows = []HetznerDailyStat{}
	}
	return rows, nil
}

// DailyHistoryByDate 取最近 days 天、按日期汇总的全部服务器日流量
// （归档行 server_id=0 也计入，用于 daily_stats 的“已删除/重建累计”行）。
func (db *DB) DailyHistoryByDate(days int) (map[string]int64, error) {
	if days <= 0 {
		days = 30
	}
	type row struct {
		Date  string `db:"date"`
		Bytes int64  `db:"bytes"`
	}
	var rows []row
	// 只取最近 days 个不同日期
	if err := db.Select(&rows, `
		SELECT date, SUM(bytes) AS bytes FROM hetzner_daily_stats
		WHERE date IN (SELECT DISTINCT date FROM hetzner_daily_stats ORDER BY date DESC LIMIT ?)
		GROUP BY date ORDER BY date ASC`, days); err != nil {
		return nil, fmt.Errorf("daily history: %w", err)
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Date] = r.Bytes
	}
	return out, nil
}

// UpsertHetznerQBNode 保存 qB 节点配置，密码加密后落盘。
func (db *DB) UpsertHetznerQBNode(n HetznerQBNode) error {
	_, err := db.Exec(`
		INSERT INTO hetzner_qb_nodes (server_id, url, username, password, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(server_id) DO UPDATE SET
		  url = excluded.url,
		  username = excluded.username,
		  password = excluded.password,
		  updated_at = excluded.updated_at`,
		n.ServerID, n.URL, n.Username, secret.Encrypt(n.Password), nowISO())
	if err != nil {
		return fmt.Errorf("upsert hetzner qb node %d: %w", n.ServerID, err)
	}
	return nil
}

// ListHetznerQBNodes 列出全部 qB 节点（密码解密后返回；解密失败的节点密码置空）。
func (db *DB) ListHetznerQBNodes() ([]HetznerQBNode, error) {
	type row struct {
		ServerID int64  `db:"server_id"`
		URL      string `db:"url"`
		Username string `db:"username"`
		Password string `db:"password"`
	}
	var rows []row
	if err := db.Select(&rows, `SELECT server_id, url, username, password FROM hetzner_qb_nodes ORDER BY server_id`); err != nil {
		return nil, fmt.Errorf("list hetzner qb nodes: %w", err)
	}
	out := make([]HetznerQBNode, 0, len(rows))
	for _, r := range rows {
		pw, err := secret.Decrypt(r.Password)
		if err != nil {
			pw = ""
		}
		out = append(out, HetznerQBNode{ServerID: r.ServerID, URL: r.URL, Username: r.Username, Password: pw})
	}
	return out, nil
}

// DeleteHetznerQBNode 按 server_id 删 qB 节点。
func (db *DB) DeleteHetznerQBNode(serverID int64) error {
	_, err := db.Exec(`DELETE FROM hetzner_qb_nodes WHERE server_id = ?`, serverID)
	return err
}
