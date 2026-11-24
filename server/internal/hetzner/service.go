package hetzner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ovh-buy/server/internal/db"
	"github.com/ovh-buy/server/internal/logger"
)

// ServerRow GET /api/hetzner/servers 的一行。
type ServerRow struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	ServerType string `json:"server_type"`
	Location   string `json:"location"`
	IPv4       string `json:"ipv4"`
	IPv6       string `json:"ipv6"`
	Created    string `json:"created"`
	Traffic    struct {
		UsedBytes  uint64  `json:"used_bytes"`
		LimitBytes uint64  `json:"limit_bytes"`
		Percent    float64 `json:"percent"`
	} `json:"traffic"`
	QBConfigured bool `json:"qb_configured"`
	// TodayBytes 今天出流量（metrics 口径，失败时为 0）。
	TodayBytes int64 `json:"today_bytes"`
}

// CheckResult POST /api/hetzner/check 的一次评估结果。
type CheckResult struct {
	CheckedAt string        `json:"checked_at"`
	SafeMode  bool          `json:"safe_mode"`
	Actions   []CheckAction `json:"actions"`
	Errors    []string      `json:"errors,omitempty"`
}

// CheckAction 一台服务器的评估动作。
type CheckAction struct {
	ServerID   int64   `json:"server_id"`
	ServerName string  `json:"server_name"`
	Action     string  `json:"action"`
	Reason     string  `json:"reason"`
	UsedTB     float64 `json:"used_tb"`
	LimitTB    float64 `json:"limit_tb"`
}

// EstimateSnapshot 快照费用估算结果。
type EstimateSnapshot struct {
	ServerID            int64   `json:"server_id"`
	ServerName          string  `json:"server_name"`
	DiskGB              float64 `json:"disk_gb"`
	EstimatedSizeGB     float64 `json:"estimated_snapshot_size_gb"`
	SnapshotPricePerGB  float64 `json:"snapshot_price_per_gb"`
	EstimatedMonthlyEUR float64 `json:"estimated_monthly_eur"`
	EstimationNote      string  `json:"estimation_note"`
}

// Service Hetzner 业务服务：客户端 + 设置 + 存储 + 通知的组装点。
// Notify 由 main 注入（走项目现有的 notify.Broadcast）；测试里可传 nil。
type Service struct {
	database *db.DB
	settings *SettingsStore
	log      *logger.Logger
	notify   func(string)

	// NewClient 可注入（默认 NewHcloudClient）；测试里换 FakeClient。
	NewClient func(token string) Client
	// QBFetch 可注入（默认 FetchQBStats）。
	QBFetch func(ctx context.Context, url, username, password string) (QBStats, error)
	// Now 可注入（默认 time.Now），测试用固定时间。
	Now func() time.Time

	jobs *JobRegistry

	loopMu      sync.Mutex
	loopRunning bool
	loopGen     int64
	loopCancel  context.CancelFunc
}

// NewService 构造。database 为 nil 时仅内存逻辑可用（单测里传真实 temp DB）。
func NewService(database *db.DB, log *logger.Logger, notify func(string)) *Service {
	return &Service{
		database:  database,
		settings:  NewSettingsStore(database),
		log:       log,
		notify:    notify,
		NewClient: NewHcloudClient,
		QBFetch:   FetchQBStats,
		Now:       time.Now,
		jobs:      NewJobRegistry(),
	}
}

func (s *Service) logInfo(msg string) {
	if s.log != nil {
		s.log.Info("[hetzner] "+msg, "hetzner")
	}
}

func (s *Service) logWarn(msg string) {
	if s.log != nil {
		s.log.Warn("[hetzner] "+msg, "hetzner")
	}
}

func (s *Service) sendNotify(msg string) {
	if s.notify != nil {
		s.notify(msg)
	}
}

// Settings 读当前设置。
func (s *Service) Settings() Settings { return s.settings.Load() }

// SaveSettings 保存设置；返回是否配了 token（供调用方决定是否重启后台循环）。
func (s *Service) SaveSettings(in Settings) error { return s.settings.Save(in) }

// Client 按当前 token 建客户端；未配 token 返回 ErrNoToken。
func (s *Service) Client() (Client, error) {
	st := s.settings.Load()
	if strings.TrimSpace(st.Token) == "" {
		return nil, ErrNoToken
	}
	return s.NewClient(st.Token), nil
}

// Configured 是否已配 token。
func (s *Service) Configured() bool {
	return strings.TrimSpace(s.settings.Load().Token) != ""
}

// Jobs job 注册表。
func (s *Service) Jobs() *JobRegistry { return s.jobs }

// RunJob 启动后台任务，返回 job id。
func (s *Service) RunJob(kind string, fn func() (interface{}, error)) string {
	return s.jobs.Run(kind, fn)
}

// todayMonth 当前 UTC 日期与月份。
func (s *Service) todayMonth() (string, string) {
	now := s.Now().UTC()
	return now.Format("2006-01-02"), now.Format("2006-01")
}

// RolloverTotals 当前沉淀累计。
func (s *Service) RolloverTotals() RolloverState {
	today, month := s.todayMonth()
	return s.settings.RolloverTotals(today, month)
}

// CollectServers 列出服务器并算好流量（含今日流量，失败时该台为 0，不阻塞整表）。
func (s *Service) CollectServers(ctx context.Context) ([]ServerRow, error) {
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	servers, err := client.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	st := s.settings.Load()
	policies, _ := s.database.ListHetznerPolicies()
	polByID := map[int64]db.HetznerPolicy{}
	for _, p := range policies {
		polByID[p.ServerID] = p
	}
	qbNodes, _ := s.database.ListHetznerQBNodes()
	qbByID := map[int64]bool{}
	for _, n := range qbNodes {
		qbByID[n.ServerID] = true
	}
	rows := make([]ServerRow, 0, len(servers))
	for _, srv := range servers {
		row := ServerRow{
			ID: srv.ID, Name: srv.Name, Status: srv.Status,
			ServerType: srv.ServerType, Location: srv.Location,
			IPv4: srv.IPv4, IPv6: srv.IPv6,
			Created:      srv.Created.Format(time.RFC3339),
			QBConfigured: qbByID[srv.ID],
		}
		limit := srv.IncludedTraffic
		if limit == 0 {
			limit = uint64(st.TrafficLimitTB * float64(BytesInTB))
		}
		row.Traffic.UsedBytes = srv.OutgoingTraffic
		row.Traffic.LimitBytes = limit
		if limit > 0 {
			row.Traffic.Percent = float64(srv.OutgoingTraffic) / float64(limit)
		}
		_ = polByID
		if today, err := client.GetOutboundTodayBytes(ctx, srv.ID); err == nil {
			row.TodayBytes = today
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, k int) bool { return rows[i].ID < rows[k].ID })
	return rows, nil
}

// GetServer 取一台服务器。
func (s *Service) GetServer(ctx context.Context, id int64) (*ServerInfo, error) {
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	return client.GetServer(ctx, id)
}

// DailyTraffic 某台服务器的日流量序列（metrics 口径），并落库到日统计表。
// 附带归档行（已删除/重建累计，server_id=0 的汇总）。
func (s *Service) DailyTraffic(ctx context.Context, serverID int64, days int) ([]TrafficPoint, []TrafficPoint, error) {
	if days <= 0 {
		days = 30
	}
	if days > 90 {
		days = 90
	}
	client, err := s.Client()
	if err != nil {
		return nil, nil, err
	}
	points, err := client.GetOutboundDaily(ctx, serverID, days)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range points {
		_ = s.database.UpsertHetznerDailyStat(serverID, p.Date, p.Bytes)
	}
	hist, err := s.database.DailyHistoryByDate(days)
	if err != nil {
		return points, nil, nil
	}
	archived := make([]TrafficPoint, 0, len(hist))
	for d, b := range hist {
		archived = append(archived, TrafficPoint{Date: d, Bytes: b})
	}
	sort.Slice(archived, func(i, k int) bool { return archived[i].Date < archived[k].Date })
	return points, archived, nil
}

// waitAction 轮询 action 到 success/error（默认最多 10 分钟，测试里 fake 立即 success）。
func (s *Service) waitAction(ctx context.Context, client Client, actionID int64, title string) error {
	if actionID == 0 {
		return nil
	}
	deadline := time.Now().Add(10 * time.Minute)
	for {
		act, err := client.GetAction(ctx, actionID)
		if err != nil {
			return err
		}
		switch act.Status {
		case "success":
			return nil
		case "error":
			return fmt.Errorf("%s failed: %s", title, act.ErrorMessage)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s timeout: action %d", title, actionID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// settleTraffic 机器删除/重建后把当月/当日流量沉淀到累计与日历史，
// 避免 Hetzner 侧清零后统计断档（对照 hzc 的 _add_rollover + _merge_rollover_daily_history）。
func (s *Service) settleTraffic(ctx context.Context, client Client, srv *ServerInfo) {
	if srv == nil {
		return
	}
	today, month := s.todayMonth()
	dayBytes, _ := client.GetOutboundTodayBytes(ctx, srv.ID)
	_ = s.settings.AddRollover(today, month, int64(srv.OutgoingTraffic), dayBytes)
	_ = s.database.AddHetznerDailyStat(0, today, dayBytes)
	if points, err := client.GetOutboundDaily(ctx, srv.ID, 30); err == nil {
		for _, p := range points {
			if p.Date == today || p.Bytes <= 0 {
				continue
			}
			_ = s.database.AddHetznerDailyStat(0, p.Date, p.Bytes)
		}
	}
}

// migratePolicyAndQB 重建后把旧服务器的策略与 qB 节点迁移到新 id。
func (s *Service) migratePolicyAndQB(oldID, newID int64) (policyMigrated, qbMigrated bool) {
	if pol, err := s.database.GetHetznerPolicy(oldID); err == nil && pol != nil {
		np := *pol
		np.ServerID = newID
		if err := s.database.UpsertHetznerPolicy(np); err == nil {
			_ = s.database.DeleteHetznerPolicy(oldID)
			policyMigrated = true
		}
	}
	nodes, err := s.database.ListHetznerQBNodes()
	if err == nil {
		for _, n := range nodes {
			if n.ServerID == oldID {
				nn := n
				nn.ServerID = newID
				if err := s.database.UpsertHetznerQBNode(nn); err == nil {
					_ = s.database.DeleteHetznerQBNode(oldID)
					qbMigrated = true
				}
			}
		}
	}
	return policyMigrated, qbMigrated
}

// CreateServer 创建服务器：先查机型可售性（不可售直接拒绝，不重试），
// 再创建；412 resource_unavailable 时最多重试 3 次（placement 波动）。
func (s *Service) CreateServer(ctx context.Context, in CreateServerInput) (*CreateServerResult, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("name is required")
	}
	if strings.TrimSpace(in.ServerType) == "" {
		return nil, errors.New("server_type is required")
	}
	if in.Image.IsZero() {
		return nil, errors.New("image is required")
	}
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	types, err := client.ListServerTypes(ctx)
	if err == nil {
		var target *ServerTypeInfo
		for _, t := range types {
			if t.Name == in.ServerType {
				target = t
				break
			}
		}
		if target == nil {
			return nil, fmt.Errorf("server_type %q not found", in.ServerType)
		}
		if in.Location != "" {
			sellable := false
			for _, loc := range target.SellableLocations {
				if loc == in.Location {
					sellable = true
					break
				}
			}
			if !sellable {
				return nil, fmt.Errorf("server_type %s is not sellable in location %s", in.ServerType, in.Location)
			}
		}
	}
	var res *CreateServerResult
	var lastErr error
	for i := 0; i < 3; i++ {
		res, lastErr = client.CreateServer(ctx, in)
		if lastErr == nil {
			break
		}
		if !errIsResourceUnavailable(lastErr) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(2+i*2) * time.Second):
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	s.sendNotify(fmt.Sprintf("🆕 Hetzner 新服务器已创建：%s (ID: %d)", res.Server.Name, res.Server.ID))
	return res, nil
}

// Rebuild 保留原 IP 的重建：删旧机 → 用同配置 + 原 Primary IP 建新机。
// image 为空时先给旧机打快照，用快照重建（与 hzc rebuild_with_snapshot_manual 同名行为一致）。
// 失败时若旧机还在，走 safe 路径（关机 → 解绑 IP → 删机 → 重建）再试一次。
func (s *Service) Rebuild(ctx context.Context, serverID int64, image ImageRef) (map[string]interface{}, error) {
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	srv, err := client.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if srv.ServerType == "" || srv.Location == "" {
		return nil, errors.New("source server is missing server_type/location")
	}
	if image.IsZero() {
		desc := fmt.Sprintf("rebuild-%s-%s", srv.Name, s.Now().UTC().Format("20060102150405"))
		imageID, actionID, err := client.CreateSnapshot(ctx, serverID, desc)
		if err != nil {
			return nil, fmt.Errorf("create snapshot before rebuild: %w", err)
		}
		if err := s.waitAction(ctx, client, actionID, "snapshot"); err != nil {
			return nil, err
		}
		if imageID == 0 {
			return nil, errors.New("snapshot did not return an image id")
		}
		image = ImageRef{ID: imageID}
	}
	// 防止删机时 Primary IP 被级联删除。
	if srv.IPv4ID != 0 {
		_ = client.SetPrimaryIPAutoDelete(ctx, srv.IPv4ID, false)
	}
	if srv.IPv6ID != 0 {
		_ = client.SetPrimaryIPAutoDelete(ctx, srv.IPv6ID, false)
	}
	create := func() (*CreateServerResult, error) {
		in := CreateServerInput{
			Name: srv.Name, ServerType: srv.ServerType, Location: srv.Location, Image: image,
		}
		if srv.IPv4ID != 0 {
			v := srv.IPv4ID
			in.PrimaryIPv4ID = &v
		}
		if srv.IPv6ID != 0 {
			v := srv.IPv6ID
			in.PrimaryIPv6ID = &v
		}
		var res *CreateServerResult
		var lastErr error
		for i := 0; i < 8; i++ {
			res, lastErr = client.CreateServer(ctx, in)
			if lastErr == nil {
				return res, nil
			}
			if !errIsResourceUnavailable(lastErr) {
				return nil, lastErr
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(3+i*2) * time.Second):
			}
		}
		return nil, lastErr
	}

	path := "fast"
	settled := false
	settle := func() {
		if settled {
			return
		}
		settled = true
		s.settleTraffic(ctx, client, srv)
	}
	if err := client.DeleteServer(ctx, serverID); err != nil {
		// fast 路径删机失败：走 safe 路径（关机 → 解绑 → 删机）。
		if pof, aerr := client.ServerAction(ctx, serverID, "poweroff"); aerr == nil && pof != nil {
			_ = s.waitAction(ctx, client, pof.ID, "poweroff")
		}
		if srv.IPv4ID != 0 {
			if r, aerr := client.UnassignPrimaryIP(ctx, srv.IPv4ID); aerr == nil && r != nil {
				_ = s.waitAction(ctx, client, r.ID, "unassign ipv4")
			}
		}
		if srv.IPv6ID != 0 {
			if r, aerr := client.UnassignPrimaryIP(ctx, srv.IPv6ID); aerr == nil && r != nil {
				_ = s.waitAction(ctx, client, r.ID, "unassign ipv6")
			}
		}
		if derr := client.DeleteServer(ctx, serverID); derr != nil {
			return nil, fmt.Errorf("delete old server %d: %w", serverID, derr)
		}
		path = "safe-fallback"
	}
	settle()
	created, err := create()
	if err != nil {
		s.sendNotify(fmt.Sprintf("❌ Hetzner 重建失败\n服务器ID: %d\n镜像: %s\n错误: %s", serverID, image.String(), err.Error()))
		return nil, err
	}
	newID := created.Server.ID
	polMigrated, qbMigrated := s.migratePolicyAndQB(serverID, newID)
	s.sendNotify(fmt.Sprintf("♻️ Hetzner 重建已完成（重置流量）\n旧服务器ID: %d\n新服务器ID: %d\nIPv4: %s\n镜像: %s\n路径: %s\n策略迁移: %s\nqB迁移: %s",
		serverID, newID, created.Server.IPv4, image.String(), path,
		boolText(polMigrated, "已延用", "无/未延用"), boolText(qbMigrated, "已延用", "无/未延用")))
	return map[string]interface{}{
		"ok": true, "old_server_id": serverID, "new_server_id": newID,
		"new_ipv4": created.Server.IPv4, "image_id": image.String(), "path": path,
		"migrated_auto_policy": polMigrated, "migrated_qb": qbMigrated,
		"root_password": created.RootPassword,
	}, nil
}

// RebuildFull 完全重建（换 IP）：先按原配置建新机，再删旧机。
func (s *Service) RebuildFull(ctx context.Context, serverID int64, image ImageRef) (map[string]interface{}, error) {
	if image.IsZero() {
		return nil, errors.New("image_id is required for full rebuild")
	}
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	srv, err := client.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	created, err := client.CreateServer(ctx, CreateServerInput{
		Name: srv.Name, ServerType: srv.ServerType, Location: srv.Location, Image: image,
	})
	if err != nil {
		return nil, err
	}
	if err := client.DeleteServer(ctx, serverID); err != nil {
		return nil, fmt.Errorf("new server %d created but failed to delete old server %d: %w", created.Server.ID, serverID, err)
	}
	s.settleTraffic(ctx, client, srv)
	s.sendNotify(fmt.Sprintf("🧨 Hetzner 完全重建已完成（换IP）\n旧服务器ID: %d\n新服务器ID: %d\n新IP: %s\n镜像: %s",
		serverID, created.Server.ID, created.Server.IPv4, image.String()))
	return map[string]interface{}{
		"ok": true, "old_server_id": serverID, "new_server_id": created.Server.ID,
		"new_ipv4": created.Server.IPv4, "image_id": image.String(),
		"root_password": created.RootPassword,
	}, nil
}

// DeleteServer 删除服务器（可选先快照、可选保留 IPv4/IPv6）。
func (s *Service) DeleteServer(ctx context.Context, serverID int64, opts DeleteServerOptions) (map[string]interface{}, error) {
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	srv, err := client.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	snapshotName := ""
	if opts.CreateSnapshot {
		snapshotName = fmt.Sprintf("before-delete-%s-%s", srv.Name, s.Now().UTC().Format("20060102150405"))
		_, actionID, err := client.CreateSnapshot(ctx, serverID, snapshotName)
		if err != nil {
			return nil, fmt.Errorf("create snapshot before delete: %w", err)
		}
		if err := s.waitAction(ctx, client, actionID, "snapshot"); err != nil {
			return nil, err
		}
	}
	kept := map[string]interface{}{"ipv4": nil, "ipv6": nil}
	deletedIPs := map[string]interface{}{"ipv4": nil, "ipv6": nil}
	var warnings []string

	if opts.KeepIPv4 && srv.IPv4ID != 0 {
		_ = client.SetPrimaryIPAutoDelete(ctx, srv.IPv4ID, false)
	}
	if opts.KeepIPv6 && srv.IPv6ID != 0 {
		_ = client.SetPrimaryIPAutoDelete(ctx, srv.IPv6ID, false)
	}
	if opts.KeepIPv4 || opts.KeepIPv6 {
		// safe 模式：先关机再解绑，规避 422。
		if pof, err := client.ServerAction(ctx, serverID, "poweroff"); err == nil && pof != nil {
			_ = s.waitAction(ctx, client, pof.ID, "poweroff")
		}
		if opts.KeepIPv4 && srv.IPv4ID != 0 {
			if r, err := client.UnassignPrimaryIP(ctx, srv.IPv4ID); err == nil && r != nil {
				_ = s.waitAction(ctx, client, r.ID, "unassign ipv4")
			}
			kept["ipv4"] = srv.IPv4ID
		}
		if opts.KeepIPv6 && srv.IPv6ID != 0 {
			if r, err := client.UnassignPrimaryIP(ctx, srv.IPv6ID); err == nil && r != nil {
				_ = s.waitAction(ctx, client, r.ID, "unassign ipv6")
			}
			kept["ipv6"] = srv.IPv6ID
		}
	}
	if err := client.DeleteServer(ctx, serverID); err != nil {
		return nil, err
	}
	// 未保留的 IP 删掉（失败只记 warning，不阻断“服务器删除成功”）。
	deleteIP := func(id int64, label string) {
		if id == 0 {
			return
		}
		for i := 0; i < 4; i++ {
			if err := client.DeletePrimaryIP(ctx, id); err == nil {
				if label == "ipv4" {
					deletedIPs["ipv4"] = id
				} else {
					deletedIPs["ipv6"] = id
				}
				return
			} else if errIsNotFound(err) {
				return
			} else if i < 3 {
				time.Sleep(time.Duration(2+i*2) * time.Second)
			} else {
				warnings = append(warnings, fmt.Sprintf("%s delete failed: %s", label, err.Error()))
			}
		}
	}
	if !opts.KeepIPv4 {
		deleteIP(srv.IPv4ID, "ipv4")
	}
	if !opts.KeepIPv6 {
		deleteIP(srv.IPv6ID, "ipv6")
	}
	s.settleTraffic(ctx, client, srv)
	_ = s.database.DeleteHetznerPolicy(serverID)
	_ = s.database.DeleteHetznerQBNode(serverID)
	s.sendNotify(fmt.Sprintf("🗑️ Hetzner 服务器已删除\nID: %d\n名称: %s\n快照: %s\n保留IPv4: %s\n保留IPv6: %s",
		serverID, srv.Name, boolText(opts.CreateSnapshot, "已创建", "未创建"),
		boolText(kept["ipv4"] != nil, "是", "否"), boolText(kept["ipv6"] != nil, "是", "否")))
	return map[string]interface{}{
		"ok": true, "deleted_server_id": serverID,
		"snapshot_created": opts.CreateSnapshot, "snapshot_name": snapshotName,
		"kept_primary_ip_ids": kept, "deleted_primary_ip_ids": deletedIPs,
		"warnings": warnings,
	}, nil
}

// CreateSnapshotManual 创建快照并记本地记录。
func (s *Service) CreateSnapshotManual(ctx context.Context, serverID int64, description string) (map[string]interface{}, error) {
	if strings.TrimSpace(description) == "" {
		description = fmt.Sprintf("manual-snap-%d-%s", serverID, s.Now().UTC().Format("20060102150405"))
	}
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	imageID, actionID, err := client.CreateSnapshot(ctx, serverID, description)
	if err != nil {
		return nil, err
	}
	_ = s.database.UpsertHetznerSnapshot(db.HetznerSnapshot{
		ID: imageID, ServerID: serverID, Description: description,
		CreatedAt: s.Now().UTC().Format(time.RFC3339),
	})
	s.sendNotify(fmt.Sprintf("📸 Hetzner 快照已开始：服务器 %d（%s）", serverID, description))
	return map[string]interface{}{
		"ok": true, "message": "snapshot job started",
		"image_id": imageID, "action_id": actionID, "description": description,
	}, nil
}

// ListSnapshots 列出快照（API 为准，本地记录补 server_id）。
func (s *Service) ListSnapshots(ctx context.Context) ([]SnapshotInfo, error) {
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	snaps, err := client.ListSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	local, _ := s.database.ListHetznerSnapshots(0)
	byID := map[int64]db.HetznerSnapshot{}
	for _, l := range local {
		byID[l.ID] = l
	}
	out := make([]SnapshotInfo, 0, len(snaps))
	for _, sn := range snaps {
		cp := *sn
		if l, ok := byID[sn.ID]; ok {
			cp.ServerID = l.ServerID
			if cp.Description == "" {
				cp.Description = l.Description
			}
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Created.After(out[k].Created) })
	return out, nil
}

// DeleteSnapshotManual 删除快照（API + 本地记录）。
func (s *Service) DeleteSnapshotManual(ctx context.Context, imageID int64) error {
	client, err := s.Client()
	if err != nil {
		return err
	}
	if err := client.DeleteSnapshot(ctx, imageID); err != nil {
		return err
	}
	_ = s.database.DeleteHetznerSnapshot(imageID)
	s.sendNotify(fmt.Sprintf("🗑️ Hetzner 快照已删除：%d", imageID))
	return nil
}

// RenameSnapshotManual 改快照描述。
func (s *Service) RenameSnapshotManual(ctx context.Context, imageID int64, description string) (*SnapshotInfo, error) {
	if strings.TrimSpace(description) == "" {
		return nil, errors.New("description is required")
	}
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	sn, err := client.UpdateSnapshot(ctx, imageID, description)
	if err != nil {
		return nil, err
	}
	_ = s.database.UpsertHetznerSnapshot(db.HetznerSnapshot{
		ID: imageID, Description: description,
		CreatedAt: sn.Created.Format(time.RFC3339),
	})
	return sn, nil
}

// EstimateSnapshot 快照费用估算：有历史快照取均值，否则按磁盘 35% 估算，夹到 [1, 磁盘]。
func (s *Service) EstimateSnapshot(ctx context.Context, serverID int64) (*EstimateSnapshot, error) {
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	srv, err := client.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	diskGB := float64(srv.DiskGB)
	snaps, _ := client.ListSnapshots(ctx)
	var sum float64
	var n int
	for _, sn := range snaps {
		if sn.SizeGB > 0 {
			sum += float64(sn.SizeGB)
			n++
		}
	}
	var est float64
	if n > 0 {
		est = sum / float64(n)
	} else {
		est = diskGB * 0.35
	}
	if est < 1 {
		est = 1
	}
	if diskGB > 0 && est > diskGB {
		est = diskGB
	}
	// 四舍五入到 2 位小数。
	est = float64(int(est*100+0.5)) / 100
	price := s.settings.Load().SnapshotPricePerGB
	return &EstimateSnapshot{
		ServerID: serverID, ServerName: srv.Name, DiskGB: diskGB,
		EstimatedSizeGB: est, SnapshotPricePerGB: price,
		EstimatedMonthlyEUR: float64(int(est*price*10000+0.5)) / 10000,
		EstimationNote:      "基于历史快照均值；无历史时按磁盘35%估算",
	}, nil
}

// ResetPassword 重置密码。密码只在 API 响应里返回一次，
// 通知里不带密码（通知走 broadcast，含 webhook 通道，不宜把密码扩散过去）。
func (s *Service) ResetPassword(ctx context.Context, serverID int64) (string, error) {
	client, err := s.Client()
	if err != nil {
		return "", err
	}
	pw, _, err := client.ResetPassword(ctx, serverID)
	if err != nil {
		return "", err
	}
	if pw != "" {
		s.sendNotify(fmt.Sprintf("🔐 Hetzner 服务器密码已重置\nID: %d\n新密码请在接口返回里查看（只显示一次）", serverID))
	} else {
		s.sendNotify(fmt.Sprintf("⚠️ Hetzner 服务器 %d 已触发重置密码，但未在响应中拿到明文密码，请到 Hetzner Console 查看 Action 结果。", serverID))
	}
	return pw, nil
}

// Reboot 软重启。
func (s *Service) Reboot(ctx context.Context, serverID int64) (*ActionInfo, error) {
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	return client.ServerAction(ctx, serverID, "reboot")
}

// HardReboot 硬重启：关机 → 开机。
func (s *Service) HardReboot(ctx context.Context, serverID int64) (map[string]interface{}, error) {
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	off, err := client.ServerAction(ctx, serverID, "poweroff")
	if err != nil {
		return nil, err
	}
	time.Sleep(3 * time.Second)
	on, err := client.ServerAction(ctx, serverID, "poweron")
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"ok": true, "server_id": serverID,
		"poweroff_action": off, "poweron_action": on,
	}, nil
}

// RenameServer 改名。
func (s *Service) RenameServer(ctx context.Context, serverID int64, name string) (*ServerInfo, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("name is required")
	}
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	updated, err := client.UpdateServerName(ctx, serverID, name)
	if err != nil {
		return nil, err
	}
	s.sendNotify(fmt.Sprintf("✏️ Hetzner 服务器已改名：%d -> %s", serverID, name))
	return updated, nil
}

// GetActionStatus 查异步操作状态。
func (s *Service) GetActionStatus(ctx context.Context, actionID int64) (*ActionInfo, error) {
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	return client.GetAction(ctx, actionID)
}

// ---- 自动重建策略 ----

// ListPolicies 列出策略。
func (s *Service) ListPolicies() ([]db.HetznerPolicy, error) {
	return s.database.ListHetznerPolicies()
}

// SetPolicy 保存策略（threshold 校验 (0,1]）。
func (s *Service) SetPolicy(serverID int64, enabled bool, threshold float64, imageID string) (db.HetznerPolicy, error) {
	if threshold <= 0 || threshold > 1 {
		return db.HetznerPolicy{}, errors.New("threshold must be in (0, 1]")
	}
	p := db.HetznerPolicy{ServerID: serverID, Enabled: enabled, Threshold: threshold, ImageID: strings.TrimSpace(imageID)}
	if err := s.database.UpsertHetznerPolicy(p); err != nil {
		return db.HetznerPolicy{}, err
	}
	return p, nil
}

// DeletePolicy 删策略。
func (s *Service) DeletePolicy(serverID int64) error {
	return s.database.DeleteHetznerPolicy(serverID)
}

// EvaluatePolicies 跑一次流量检查与策略评估（后台循环与 POST /check 共用）。
// 实时校准：对配了策略的机器用 metrics 月累计再算一次，避免 list 的月流量滞后。
func (s *Service) EvaluatePolicies(ctx context.Context) (*CheckResult, error) {
	client, err := s.Client()
	if err != nil {
		return nil, err
	}
	st := s.settings.Load()
	rows, err := s.CollectServers(ctx)
	if err != nil {
		return nil, err
	}
	policies, _ := s.database.ListHetznerPolicies()
	polByID := map[int64]db.HetznerPolicy{}
	for _, p := range policies {
		polByID[p.ServerID] = p
	}
	// 实时校准
	realtime := map[int64]float64{}
	for _, row := range rows {
		if pol, ok := polByID[row.ID]; ok && pol.Enabled {
			if b, err := client.GetOutboundMonthBytes(ctx, row.ID); err == nil && b > 0 {
				realtime[row.ID] = float64(b) / float64(BytesInTB)
			}
		}
	}
	guard := s.settings.LoadGuardState()
	nowUnix := s.Now().Unix()
	res := &CheckResult{CheckedAt: s.Now().UTC().Format(time.RFC3339), SafeMode: st.SafeMode, Actions: []CheckAction{}}
	guardDirty := false
	for _, row := range rows {
		pol, hasPol := polByID[row.ID]
		threshold := st.RotateThreshold
		imageID := ""
		enabled := false
		if hasPol {
			enabled = pol.Enabled
			threshold = pol.Threshold
			imageID = pol.ImageID
		}
		usedTB := float64(row.Traffic.UsedBytes) / float64(BytesInTB)
		if v, ok := realtime[row.ID]; ok {
			usedTB = v
		}
		limitTB := float64(row.Traffic.LimitBytes) / float64(BytesInTB)
		if limitTB <= 0 {
			limitTB = st.TrafficLimitTB
		}
		entry := guard[row.ID]
		dec := DecidePolicyAction(PolicyInput{
			Enabled: enabled, Threshold: threshold, ImageID: imageID,
			UsedTB: usedTB, LimitTB: limitTB, SafeMode: st.SafeMode,
			Guard: entry, NowUnix: nowUnix,
		})
		act := CheckAction{ServerID: row.ID, ServerName: row.Name, Action: dec.Action, Reason: dec.Reason, UsedTB: dec.UsedTB, LimitTB: dec.LimitTB}
		res.Actions = append(res.Actions, act)
		switch dec.Action {
		case "none":
			// 低于限额-1TB 时清理陈旧守卫状态。
			if !enabled && usedTB < limitTB-1 && (entry.AutoStopped || entry.LastStopTS != 0) {
				entry.AutoStopped = false
				entry.LastStopTS = 0
				guard[row.ID] = entry
				guardDirty = true
			}
		case "warn":
			entry.LastWarnTS = nowUnix
			guard[row.ID] = entry
			guardDirty = true
			s.sendNotify(fmt.Sprintf("⚠️ Hetzner 流量预警：%s (ID:%d)\n当前：%.2f TB / 限额：%.2f TB\n说明：未开启自动重建策略，达到限额将自动关机", row.Name, row.ID, usedTB, limitTB))
		case "alert_threshold", "alert_no_image", "alert_poweroff":
			s.sendNotify(fmt.Sprintf("⚠️ Hetzner SAFE_MODE 告警：%s (ID:%d)\n%s\n当前：%.2f TB / 限额：%.2f TB", row.Name, row.ID, dec.Reason, usedTB, limitTB))
		case "auto_poweroff":
			if _, aerr := client.ServerAction(ctx, row.ID, "poweroff"); aerr != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("server %d auto poweroff failed: %s", row.ID, aerr.Error()))
				s.sendNotify(fmt.Sprintf("❌ Hetzner 自动关机失败：%s (ID:%d)\n错误：%s", row.Name, row.ID, aerr.Error()))
			} else {
				entry.AutoStopped = true
				entry.LastStopTS = nowUnix
				guard[row.ID] = entry
				guardDirty = true
				s.sendNotify(fmt.Sprintf("🛑 Hetzner 自动保护已执行：%s (ID:%d)\n当前：%.2f TB / 限额：%.2f TB\n动作：已自动关机（因未开启重建策略且达到限额）", row.Name, row.ID, usedTB, limitTB))
			}
		case "rebuild":
			s.sendNotify(fmt.Sprintf("🚀 Hetzner 自动重建开始：%s (ID:%d)\n当前：%.2f TB / 阈值：%.2f\n镜像/快照：%s", row.Name, row.ID, usedTB, threshold, imageID))
			if _, rerr := s.Rebuild(ctx, row.ID, ParseImageRef(imageID)); rerr != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("server %d auto rebuild failed: %s", row.ID, rerr.Error()))
			}
		}
	}
	if guardDirty {
		_ = s.settings.SaveGuardState(guard)
	}
	return res, nil
}

// ---- qBittorrent 联动（可选，默认关闭：只在配了节点时才查） ----

// ListQBNodes 列出 qB 节点（不含密码明文——调用方按需取）。
func (s *Service) ListQBNodes() ([]db.HetznerQBNode, error) {
	return s.database.ListHetznerQBNodes()
}

// SetQBNode 保存节点。先存后测（与 hzc 一致）：测不通也保留配置并返回状态。
func (s *Service) SetQBNode(ctx context.Context, serverID int64, url, username, password string) (QBStats, error) {
	if strings.TrimSpace(url) == "" {
		return QBStats{}, errors.New("url is required")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return QBStats{}, errors.New("url must start with http:// or https://")
	}
	node := db.HetznerQBNode{ServerID: serverID, URL: strings.TrimSpace(url), Username: username, Password: password}
	if err := s.database.UpsertHetznerQBNode(node); err != nil {
		return QBStats{}, err
	}
	stats, err := s.QBFetch(ctx, node.URL, node.Username, node.Password)
	if err != nil {
		return stats, err
	}
	return stats, nil
}

// DeleteQBNode 删节点。
func (s *Service) DeleteQBNode(serverID int64) error {
	return s.database.DeleteHetznerQBNode(serverID)
}

// QBStatus 各节点 qB 状态与流量（仅查询展示）。
func (s *Service) QBStatus(ctx context.Context) (map[string]QBStats, error) {
	nodes, err := s.database.ListHetznerQBNodes()
	if err != nil {
		return nil, err
	}
	out := make(map[string]QBStats, len(nodes))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range nodes {
		wg.Add(1)
		go func(n db.HetznerQBNode) {
			defer wg.Done()
			stats, err := s.QBFetch(ctx, n.URL, n.Username, n.Password)
			if err != nil && stats.Error == "" {
				stats.Error = err.Error()
				stats.Enabled = true
			}
			mu.Lock()
			out[fmt.Sprintf("%d", n.ServerID)] = stats
			mu.Unlock()
		}(n)
	}
	wg.Wait()
	return out, nil
}

// ---- 后台定时检查循环 ----

// StartLoop 启动后台检查循环（未配 token 时不启动，返回 false）。
// 参照 internal/monitor 与 internal/vps 的代际号做法：Stop 后立刻 Start 不会
// 让旧循环活下来。
func (s *Service) StartLoop() bool {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	if s.loopRunning {
		return false
	}
	if !s.Configured() {
		return false
	}
	s.loopGen++
	gen := s.loopGen
	ctx, cancel := context.WithCancel(context.Background())
	s.loopCancel = cancel
	s.loopRunning = true
	go s.loop(ctx, gen)
	s.logInfo("Hetzner 后台检查循环已启动")
	return true
}

// StopLoop 停止后台循环。
func (s *Service) StopLoop() bool {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	if !s.loopRunning {
		return false
	}
	s.loopRunning = false
	if s.loopCancel != nil {
		s.loopCancel()
	}
	return true
}

// LoopRunning 循环是否在跑。
func (s *Service) LoopRunning() bool {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	return s.loopRunning
}

// RestartLoop 设置变更后重启循环（token 被清掉时停掉）。
func (s *Service) RestartLoop() {
	s.StopLoop()
	s.StartLoop()
}

func (s *Service) loop(ctx context.Context, gen int64) {
	// 启动即跑一次，再按间隔跑。
	s.runCheckOnce(ctx)
	for {
		interval := s.settings.Load().CheckIntervalMin
		if interval < 1 {
			interval = DefaultCheckIntervalMin
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(interval) * time.Minute):
		}
		s.loopMu.Lock()
		mine := s.loopRunning && s.loopGen == gen
		s.loopMu.Unlock()
		if !mine {
			return
		}
		s.runCheckOnce(ctx)
	}
}

func (s *Service) runCheckOnce(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := s.EvaluatePolicies(cctx); err != nil {
		if errors.Is(err, ErrNoToken) {
			s.logWarn("Hetzner 检查跳过：未配置 token")
			return
		}
		s.logWarn("Hetzner 检查失败：" + err.Error())
	}
}

func boolText(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
