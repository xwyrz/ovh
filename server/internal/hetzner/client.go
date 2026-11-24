package hetzner

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// hcloudClient 基于官方 hcloud-go SDK 的真实实现。只通过它访问 Hetzner API。
type hcloudClient struct {
	api *hcloud.Client
}

// NewHcloudClient 用 token 建一个真实客户端（不发任何请求）。
func NewHcloudClient(token string) Client {
	return &hcloudClient{api: hcloud.NewClient(hcloud.WithToken(token))}
}

func toServerInfo(s *hcloud.Server) *ServerInfo {
	if s == nil {
		return nil
	}
	out := &ServerInfo{
		ID:              s.ID,
		Name:            s.Name,
		Status:          string(s.Status),
		IncludedTraffic: s.IncludedTraffic,
		OutgoingTraffic: s.OutgoingTraffic,
		Created:         s.Created,
		Labels:          s.Labels,
	}
	if s.ServerType != nil {
		out.ServerType = s.ServerType.Name
		out.Cores = s.ServerType.Cores
		out.MemoryGB = s.ServerType.Memory
		out.DiskGB = s.ServerType.Disk
	}
	if s.Datacenter != nil {
		out.Datacenter = s.Datacenter.Name
		if s.Datacenter.Location != nil {
			out.Location = s.Datacenter.Location.Name
		}
	}
	if !s.PublicNet.IPv4.IsUnspecified() {
		out.IPv4 = s.PublicNet.IPv4.IP.String()
		out.IPv4ID = s.PublicNet.IPv4.ID
	}
	if s.PublicNet.IPv6.IP != nil && !s.PublicNet.IPv6.IP.IsUnspecified() {
		out.IPv6 = s.PublicNet.IPv6.IP.String()
		out.IPv6ID = s.PublicNet.IPv6.ID
	}
	return out
}

func toActionInfo(a *hcloud.Action) *ActionInfo {
	if a == nil {
		return nil
	}
	return &ActionInfo{ID: a.ID, Status: string(a.Status), Command: a.Command, Progress: a.Progress, ErrorMessage: a.ErrorMessage}
}

func (c *hcloudClient) ListServers(ctx context.Context) ([]*ServerInfo, error) {
	servers, err := c.api.Server.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	out := make([]*ServerInfo, 0, len(servers))
	for _, s := range servers {
		out = append(out, toServerInfo(s))
	}
	return out, nil
}

func (c *hcloudClient) GetServer(ctx context.Context, id int64) (*ServerInfo, error) {
	s, _, err := c.api.Server.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get server %d: %w", id, err)
	}
	if s == nil {
		return nil, fmt.Errorf("server %d not found", id)
	}
	return toServerInfo(s), nil
}

func (c *hcloudClient) CreateServer(ctx context.Context, in CreateServerInput) (*CreateServerResult, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("server name is required")
	}
	if strings.TrimSpace(in.ServerType) == "" {
		return nil, errors.New("server_type is required")
	}
	if in.Image.IsZero() {
		return nil, errors.New("image is required")
	}
	opts := hcloud.ServerCreateOpts{
		Name:       in.Name,
		ServerType: &hcloud.ServerType{Name: in.ServerType},
		Image:      &hcloud.Image{ID: in.Image.ID, Name: in.Image.Name},
	}
	if in.Location != "" {
		opts.Location = &hcloud.Location{Name: in.Location}
	}
	for _, k := range in.SSHKeys {
		opts.SSHKeys = append(opts.SSHKeys, &hcloud.SSHKey{ID: k})
	}
	if in.PrimaryIPv4ID != nil || in.PrimaryIPv6ID != nil {
		pn := &hcloud.ServerCreatePublicNet{EnableIPv4: true, EnableIPv6: true}
		if in.PrimaryIPv4ID != nil {
			pn.IPv4 = &hcloud.PrimaryIP{ID: *in.PrimaryIPv4ID}
		}
		if in.PrimaryIPv6ID != nil {
			pn.IPv6 = &hcloud.PrimaryIP{ID: *in.PrimaryIPv6ID}
		}
		opts.PublicNet = pn
	}
	res, _, err := c.api.Server.Create(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("create server: %w", err)
	}
	return &CreateServerResult{Server: toServerInfo(res.Server), RootPassword: res.RootPassword, ActionID: actionID(res.Action)}, nil
}

func actionID(a *hcloud.Action) int64 {
	if a == nil {
		return 0
	}
	return a.ID
}

func (c *hcloudClient) DeleteServer(ctx context.Context, id int64) error {
	s, _, err := c.api.Server.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("delete server %d: %w", id, err)
	}
	if s == nil {
		return fmt.Errorf("server %d not found", id)
	}
	if _, err := c.api.Server.Delete(ctx, s); err != nil {
		return fmt.Errorf("delete server %d: %w", id, err)
	}
	return nil
}

func (c *hcloudClient) UpdateServerName(ctx context.Context, id int64, name string) (*ServerInfo, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("name is required")
	}
	s, _, err := c.api.Server.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("rename server %d: %w", id, err)
	}
	if s == nil {
		return nil, fmt.Errorf("server %d not found", id)
	}
	updated, _, err := c.api.Server.Update(ctx, s, hcloud.ServerUpdateOpts{Name: name})
	if err != nil {
		return nil, fmt.Errorf("rename server %d: %w", id, err)
	}
	return toServerInfo(updated), nil
}

func (c *hcloudClient) ServerAction(ctx context.Context, id int64, action string) (*ActionInfo, error) {
	s, _, err := c.api.Server.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("server action %s %d: %w", action, id, err)
	}
	if s == nil {
		return nil, fmt.Errorf("server %d not found", id)
	}
	var act *hcloud.Action
	switch action {
	case "poweron":
		act, _, err = c.api.Server.Poweron(ctx, s)
	case "poweroff":
		act, _, err = c.api.Server.Poweroff(ctx, s)
	case "reboot":
		act, _, err = c.api.Server.Reboot(ctx, s)
	case "shutdown":
		act, _, err = c.api.Server.Shutdown(ctx, s)
	case "reset":
		act, _, err = c.api.Server.Reset(ctx, s)
	default:
		return nil, fmt.Errorf("unsupported server action %q", action)
	}
	if err != nil {
		return nil, fmt.Errorf("server action %s %d: %w", action, id, err)
	}
	return toActionInfo(act), nil
}

func (c *hcloudClient) ResetPassword(ctx context.Context, id int64) (string, *ActionInfo, error) {
	s, _, err := c.api.Server.GetByID(ctx, id)
	if err != nil {
		return "", nil, fmt.Errorf("reset password %d: %w", id, err)
	}
	if s == nil {
		return "", nil, fmt.Errorf("server %d not found", id)
	}
	res, _, err := c.api.Server.ResetPassword(ctx, s)
	if err != nil {
		return "", nil, fmt.Errorf("reset password %d: %w", id, err)
	}
	return res.RootPassword, toActionInfo(res.Action), nil
}

func (c *hcloudClient) RebuildServer(ctx context.Context, id int64, image ImageRef) (*ActionInfo, string, error) {
	s, _, err := c.api.Server.GetByID(ctx, id)
	if err != nil {
		return nil, "", fmt.Errorf("rebuild server %d: %w", id, err)
	}
	if s == nil {
		return nil, "", fmt.Errorf("server %d not found", id)
	}
	res, _, err := c.api.Server.RebuildWithResult(ctx, s, hcloud.ServerRebuildOpts{Image: &hcloud.Image{ID: image.ID, Name: image.Name}})
	if err != nil {
		return nil, "", fmt.Errorf("rebuild server %d: %w", id, err)
	}
	return toActionInfo(res.Action), res.RootPassword, nil
}

func (c *hcloudClient) CreateSnapshot(ctx context.Context, serverID int64, description string) (int64, int64, error) {
	s, _, err := c.api.Server.GetByID(ctx, serverID)
	if err != nil {
		return 0, 0, fmt.Errorf("create snapshot %d: %w", serverID, err)
	}
	if s == nil {
		return 0, 0, fmt.Errorf("server %d not found", serverID)
	}
	desc := description
	res, _, err := c.api.Server.CreateImage(ctx, s, &hcloud.ServerCreateImageOpts{Type: hcloud.ImageTypeSnapshot, Description: &desc})
	if err != nil {
		return 0, 0, fmt.Errorf("create snapshot %d: %w", serverID, err)
	}
	var imageID int64
	if res.Image != nil {
		imageID = res.Image.ID
	}
	return imageID, actionID(res.Action), nil
}

func (c *hcloudClient) ListSnapshots(ctx context.Context) ([]*SnapshotInfo, error) {
	images, err := c.api.Image.AllWithOpts(ctx, hcloud.ImageListOpts{Type: []hcloud.ImageType{hcloud.ImageTypeSnapshot}})
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	out := make([]*SnapshotInfo, 0, len(images))
	for _, img := range images {
		var sid int64
		if img.CreatedFrom != nil {
			sid = img.CreatedFrom.ID
		}
		out = append(out, &SnapshotInfo{ID: img.ID, Description: img.Description, SizeGB: img.ImageSize, Created: img.Created, ServerID: sid})
	}
	return out, nil
}

func (c *hcloudClient) DeleteSnapshot(ctx context.Context, id int64) error {
	img, _, err := c.api.Image.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("delete snapshot %d: %w", id, err)
	}
	if img == nil {
		return fmt.Errorf("snapshot %d not found", id)
	}
	if _, err := c.api.Image.Delete(ctx, img); err != nil {
		return fmt.Errorf("delete snapshot %d: %w", id, err)
	}
	return nil
}

func (c *hcloudClient) UpdateSnapshot(ctx context.Context, id int64, description string) (*SnapshotInfo, error) {
	img, _, err := c.api.Image.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("rename snapshot %d: %w", id, err)
	}
	if img == nil {
		return nil, fmt.Errorf("snapshot %d not found", id)
	}
	updated, _, err := c.api.Image.Update(ctx, img, hcloud.ImageUpdateOpts{Description: &description})
	if err != nil {
		return nil, fmt.Errorf("rename snapshot %d: %w", id, err)
	}
	return &SnapshotInfo{ID: updated.ID, Description: updated.Description, SizeGB: updated.ImageSize, Created: updated.Created}, nil
}

func (c *hcloudClient) GetAction(ctx context.Context, id int64) (*ActionInfo, error) {
	a, _, err := c.api.Action.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get action %d: %w", id, err)
	}
	if a == nil {
		return nil, fmt.Errorf("action %d not found", id)
	}
	return toActionInfo(a), nil
}

func (c *hcloudClient) ListServerTypes(ctx context.Context) ([]*ServerTypeInfo, error) {
	types, err := c.api.ServerType.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list server types: %w", err)
	}
	out := make([]*ServerTypeInfo, 0, len(types))
	for _, t := range types {
		info := &ServerTypeInfo{Name: t.Name, Cores: t.Cores, MemoryGB: t.Memory, DiskGB: t.Disk, MonthlyGrossEUR: map[string]float64{}}
		for _, p := range t.Pricings {
			if p.Location == nil {
				continue
			}
			info.SellableLocations = append(info.SellableLocations, p.Location.Name)
			if gross, err := strconv.ParseFloat(p.Monthly.Gross, 64); err == nil {
				info.MonthlyGrossEUR[p.Location.Name] = gross
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// metricsSeries 取 network 出流量序列：新口径 network.0.bandwidth.out（bytes/s，
// 需 ×step），兼容老口径 network.0.tx（已是 bytes）。
func (c *hcloudClient) metricsSeries(ctx context.Context, serverID int64, start, end time.Time, step int) (map[string][]hcloud.ServerMetricsValue, float64, error) {
	s, _, err := c.api.Server.GetByID(ctx, serverID)
	if err != nil {
		return nil, 0, fmt.Errorf("get metrics %d: %w", serverID, err)
	}
	if s == nil {
		return nil, 0, fmt.Errorf("server %d not found", serverID)
	}
	m, _, err := c.api.Server.GetMetrics(ctx, s, hcloud.ServerGetMetricsOpts{
		Types: []hcloud.ServerMetricType{hcloud.ServerMetricNetwork},
		Start: start, End: end, Step: step,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("get metrics %d: %w", serverID, err)
	}
	return m.TimeSeries, m.Step, nil
}

func sumSeries(ts map[string][]hcloud.ServerMetricsValue, step float64) (points []TrafficPoint, total int64) {
	series, mode := pickOutboundSeries(ts)
	byDay := map[string]int64{}
	for _, p := range series {
		v, err := strconv.ParseFloat(p.Value, 64)
		if err != nil || math.IsNaN(v) {
			continue
		}
		var b int64
		if mode == "bandwidth" {
			b = int64(v * step)
		} else {
			b = int64(v)
		}
		if b < 0 {
			continue
		}
		day := time.Unix(int64(p.Timestamp), 0).UTC().Format("2006-01-02")
		byDay[day] += b
		total += b
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range days {
		points = append(points, TrafficPoint{Date: d, Bytes: byDay[d]})
	}
	return points, total
}

func pickOutboundSeries(ts map[string][]hcloud.ServerMetricsValue) ([]hcloud.ServerMetricsValue, string) {
	if v, ok := ts["network.0.bandwidth.out"]; ok {
		return v, "bandwidth"
	}
	if v, ok := ts["network.0.tx"]; ok {
		return v, "tx"
	}
	return nil, "unknown"
}

func (c *hcloudClient) GetOutboundDaily(ctx context.Context, serverID int64, days int) ([]TrafficPoint, error) {
	if days <= 0 {
		days = 30
	}
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -days)
	series, step, err := c.metricsSeries(ctx, serverID, start, end, 3600)
	if err != nil {
		return nil, err
	}
	points, _ := sumSeries(series, step)
	return points, nil
}

func (c *hcloudClient) GetOutboundTodayBytes(ctx context.Context, serverID int64) (int64, error) {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	series, step, err := c.metricsSeries(ctx, serverID, start, now, 300)
	if err != nil {
		return 0, err
	}
	_, total := sumSeries(series, step)
	return total, nil
}

func (c *hcloudClient) GetOutboundMonthBytes(ctx context.Context, serverID int64) (int64, error) {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	series, step, err := c.metricsSeries(ctx, serverID, start, now, 3600)
	if err != nil {
		return 0, err
	}
	_, total := sumSeries(series, step)
	return total, nil
}

func (c *hcloudClient) SetPrimaryIPAutoDelete(ctx context.Context, id int64, autoDelete bool) error {
	ip, _, err := c.api.PrimaryIP.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get primary ip %d: %w", id, err)
	}
	if ip == nil {
		return fmt.Errorf("primary ip %d not found", id)
	}
	if _, _, err := c.api.PrimaryIP.Update(ctx, ip, hcloud.PrimaryIPUpdateOpts{AutoDelete: &autoDelete}); err != nil {
		return fmt.Errorf("update primary ip %d: %w", id, err)
	}
	return nil
}

func (c *hcloudClient) UnassignPrimaryIP(ctx context.Context, id int64) (*ActionInfo, error) {
	act, _, err := c.api.PrimaryIP.Unassign(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("unassign primary ip %d: %w", id, err)
	}
	return toActionInfo(act), nil
}

func (c *hcloudClient) DeletePrimaryIP(ctx context.Context, id int64) error {
	ip, _, err := c.api.PrimaryIP.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("delete primary ip %d: %w", id, err)
	}
	if ip == nil {
		return nil // 已删，幂等
	}
	if _, err := c.api.PrimaryIP.Delete(ctx, ip); err != nil {
		return fmt.Errorf("delete primary ip %d: %w", id, err)
	}
	return nil
}

// errIsNotFound 判断是否为 Hetzner 的 404（hcloud.Error 带 Code）。
func errIsNotFound(err error) bool {
	var herr *hcloud.Error
	if errors.As(err, &herr) {
		return herr.Code == hcloud.ErrorCodeNotFound
	}
	return false
}

// errIsResourceUnavailable resource_unavailable（placement 波动，可重试）。
func errIsResourceUnavailable(err error) bool {
	var herr *hcloud.Error
	if errors.As(err, &herr) {
		return herr.Code == hcloud.ErrorCodeResourceUnavailable
	}
	// FakeClient 与非 SDK 错误用错误文本兜底。
	return strings.Contains(err.Error(), "resource_unavailable")
}
