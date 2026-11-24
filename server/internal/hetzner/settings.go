package hetzner

import (
	"errors"
	"fmt"

	"github.com/ovh-buy/server/internal/db"
	"github.com/ovh-buy/server/internal/secret"
)

// 默认值（与任务约定一致；注意 hzc 的 config.py 里 ROTATE_THRESHOLD 默认是 0.9，
// 这里按任务要求用 0.98）。
const (
	DefaultTrafficLimitTB   = 20.0
	DefaultRotateThreshold  = 0.98
	DefaultCheckIntervalMin = 5
	DefaultSafeMode         = true
	kvSettingsKey           = "hetzner_settings"
	kvRolloverKey           = "hetzner_rollover"
	kvGuardStateKey         = "hetzner_guard_state"
	maxCheckIntervalMin     = 1440
	maxTrafficLimitTB       = 1000.0
)

// Settings Hetzner 设置。Token 永不进 JSON 响应、不进日志。
type Settings struct {
	Token              string  `json:"-"`
	TrafficLimitTB     float64 `json:"traffic_limit_tb"`
	RotateThreshold    float64 `json:"rotate_threshold"`
	CheckIntervalMin   int     `json:"check_interval_minutes"`
	SafeMode           bool    `json:"safe_mode"`
	SnapshotPricePerGB float64 `json:"snapshot_price_per_gb"`
}

// DefaultSettings 默认设置。
func DefaultSettings() Settings {
	return Settings{
		TrafficLimitTB:     DefaultTrafficLimitTB,
		RotateThreshold:    DefaultRotateThreshold,
		CheckIntervalMin:   DefaultCheckIntervalMin,
		SafeMode:           DefaultSafeMode,
		SnapshotPricePerGB: SnapshotPricePerGB,
	}
}

// Validate 校验设置值（token 除外：空表示未配，不算错）。
func ValidateSettings(s Settings) error {
	if s.TrafficLimitTB <= 0 || s.TrafficLimitTB > maxTrafficLimitTB {
		return fmt.Errorf("traffic_limit_tb must be > 0 and <= %v", maxTrafficLimitTB)
	}
	if s.RotateThreshold <= 0 || s.RotateThreshold > 1 {
		return errors.New("rotate_threshold must be in (0, 1]")
	}
	if s.CheckIntervalMin < 1 || s.CheckIntervalMin > maxCheckIntervalMin {
		return fmt.Errorf("check_interval_minutes must be between 1 and %d", maxCheckIntervalMin)
	}
	if s.SnapshotPricePerGB < 0 {
		return errors.New("snapshot_price_per_gb must be >= 0")
	}
	return nil
}

// settingsRecord kv 里的落盘形态：token 只存 secret 加密后的密文。
type settingsRecord struct {
	TokenEnc           string  `json:"token_enc"`
	TrafficLimitTB     float64 `json:"traffic_limit_tb"`
	RotateThreshold    float64 `json:"rotate_threshold"`
	CheckIntervalMin   int     `json:"check_interval_minutes"`
	SafeMode           bool    `json:"safe_mode"`
	SnapshotPricePerGB float64 `json:"snapshot_price_per_gb"`
}

// SettingsStore 设置的 SQLite（kv）存取。
// 没有复用 internal/config.Store 的 kv['config']：那张表会被 GET /api/settings
// 整份下发，token 放进去会泄漏；单独的 kv key + secret 加密与那边的凭据同级保护。
type SettingsStore struct {
	database *db.DB
}

// NewSettingsStore 构造。
func NewSettingsStore(database *db.DB) *SettingsStore { return &SettingsStore{database: database} }

// Load 读设置（不存在返回默认值；数值字段为 0 时按“没设”补默认）。
func (s *SettingsStore) Load() Settings {
	out := DefaultSettings()
	if s.database == nil {
		return out
	}
	var rec settingsRecord
	ok, err := s.database.GetKV(kvSettingsKey, &rec)
	if err != nil || !ok {
		return out
	}
	if rec.TokenEnc != "" {
		if plain, err := secret.Decrypt(rec.TokenEnc); err == nil {
			out.Token = plain
		}
	}
	if rec.TrafficLimitTB > 0 {
		out.TrafficLimitTB = rec.TrafficLimitTB
	}
	if rec.RotateThreshold > 0 {
		out.RotateThreshold = rec.RotateThreshold
	}
	if rec.CheckIntervalMin > 0 {
		out.CheckIntervalMin = rec.CheckIntervalMin
	}
	out.SafeMode = rec.SafeMode
	if rec.SnapshotPricePerGB > 0 {
		out.SnapshotPricePerGB = rec.SnapshotPricePerGB
	}
	return out
}

// Save 保存设置。Token 为空表示“不改”（前端 PUT 时空 token 即此语义）；
// 要清 token 目前只能在库里删 kv 行，这里不提供——避免一次误提交把凭据清掉。
func (s *SettingsStore) Save(in Settings) error {
	if err := ValidateSettings(in); err != nil {
		return err
	}
	prev := s.Load()
	rec := settingsRecord{
		TrafficLimitTB:     in.TrafficLimitTB,
		RotateThreshold:    in.RotateThreshold,
		CheckIntervalMin:   in.CheckIntervalMin,
		SafeMode:           in.SafeMode,
		SnapshotPricePerGB: in.SnapshotPricePerGB,
	}
	if in.Token != "" {
		rec.TokenEnc = secret.Encrypt(in.Token)
	} else if prev.Token != "" {
		rec.TokenEnc = secret.Encrypt(prev.Token)
	}
	if s.database == nil {
		return errors.New("database is not available")
	}
	return s.database.SetKV(kvSettingsKey, rec)
}

// RolloverState 删除/重建后沉淀下来的流量累计（hzc 的 traffic_rollover）。
// 月份/日期一切换就清零，与 hzc 的 _rollover_totals 行为一致。
type RolloverState struct {
	Month      string `json:"month"`
	MonthBytes int64  `json:"month_bytes"`
	Day        string `json:"day"`
	DayBytes   int64  `json:"day_bytes"`
}

// RolloverTotals 取当前累计（按今天做月份/日期归零判断，不写库）。
func (s *SettingsStore) RolloverTotals(today, month string) RolloverState {
	st := RolloverState{Month: month, Day: today}
	if s.database == nil {
		return st
	}
	var saved RolloverState
	ok, err := s.database.GetKV(kvRolloverKey, &saved)
	if err != nil || !ok {
		return st
	}
	if saved.Month == month {
		st.MonthBytes = saved.MonthBytes
	}
	if saved.Day == today {
		st.DayBytes = saved.DayBytes
	}
	return st
}

// AddRollover 累加删除/重建沉淀的流量（月份/日期切换时先清零再加）。
func (s *SettingsStore) AddRollover(today, month string, monthBytes, dayBytes int64) error {
	if s.database == nil {
		return errors.New("database is not available")
	}
	st := s.RolloverTotals(today, month)
	st.MonthBytes += monthBytes
	st.DayBytes += dayBytes
	return s.database.SetKV(kvRolloverKey, st)
}

// GuardEntry 一台服务器的流量守卫状态（节流与“已自动关机”去重）。
type GuardEntry struct {
	AutoStopped bool  `json:"auto_stopped"`
	LastStopTS  int64 `json:"last_stop_ts"`
	LastWarnTS  int64 `json:"last_warn_ts"`
}

// LoadGuardState 读全部守卫状态。
func (s *SettingsStore) LoadGuardState() map[int64]GuardEntry {
	out := map[int64]GuardEntry{}
	if s.database == nil {
		return out
	}
	var raw map[string]GuardEntry
	ok, err := s.database.GetKV(kvGuardStateKey, &raw)
	if err != nil || !ok {
		return out
	}
	for k, v := range raw {
		var id int64
		if _, err := fmt.Sscanf(k, "%d", &id); err == nil {
			out[id] = v
		}
	}
	return out
}

// SaveGuardState 保存全部守卫状态。
func (s *SettingsStore) SaveGuardState(state map[int64]GuardEntry) error {
	if s.database == nil {
		return errors.New("database is not available")
	}
	raw := make(map[string]GuardEntry, len(state))
	for id, v := range state {
		raw[fmt.Sprintf("%d", id)] = v
	}
	return s.database.SetKV(kvGuardStateKey, raw)
}
