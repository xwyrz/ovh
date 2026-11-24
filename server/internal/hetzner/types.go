// Package hetzner 把 liqiba/hzc 的 Hetzner 流量保护功能干净重写为 Go 模块。
//
// 只对照 Hetzner Cloud 公开 API 文档与官方 Go SDK（hcloud-go）实现相同行为，
// 不复制参考实现（Python）的源码；结构与命名均为本项目自己的。
package hetzner

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrNoToken 未配置 Hetzner Token。调用方应把它转成 4xx（英文 message + 中文说明），
// 不要当 500、不要 panic。
var ErrNoToken = errors.New("hetzner token is not configured (Hetzner Token 未配置，请先在设置里填写)")

// BytesInTB 与 Hetzner 面板一致用二进制 TiB（UI 上标 TB）。
const BytesInTB = int64(1024) * 1024 * 1024 * 1024

// SnapshotPricePerGB 快照单价 EUR/GB/月（hzc 默认 0.011）。
const SnapshotPricePerGB = 0.011

// TrafficPoint 一日出流量。
type TrafficPoint struct {
	Date  string `json:"date"`
	Bytes int64  `json:"bytes"`
}

// ImageRef 镜像/快照引用：数字 id 或名字。
type ImageRef struct {
	ID   int64
	Name string
}

// ParseImageRef "123" → ID，otherwise → Name。空串返回零值。
func ParseImageRef(s string) ImageRef {
	s = strings.TrimSpace(s)
	if s == "" {
		return ImageRef{}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return ImageRef{ID: n}
	}
	return ImageRef{Name: s}
}

// IsZero 是否未指定。
func (r ImageRef) IsZero() bool { return r.ID == 0 && r.Name == "" }

// String 序列化回字符串形式（策略表里存的就是这个）。
func (r ImageRef) String() string {
	if r.ID != 0 {
		return strconv.FormatInt(r.ID, 10)
	}
	return r.Name
}

// ServerInfo 一台 Hetzner 服务器的归一化视图（与 hcloud 类型解耦，便于 mock）。
type ServerInfo struct {
	ID              int64
	Name            string
	Status          string
	ServerType      string
	Cores           int
	MemoryGB        float32
	DiskGB          int
	Location        string
	Datacenter      string
	IPv4            string
	IPv4ID          int64
	IPv6            string
	IPv6ID          int64
	IncludedTraffic uint64
	OutgoingTraffic uint64
	Created         time.Time
	Labels          map[string]string
}

// SnapshotInfo 一张快照（Hetzner image type=snapshot）。
type SnapshotInfo struct {
	ID          int64     `json:"id"`
	Description string    `json:"description"`
	SizeGB      float32   `json:"size_gb"`
	Created     time.Time `json:"created"`
	ServerID    int64     `json:"server_id"` // 创建来源服务器（API 不直接给，本地记录补）
}

// ServerTypeInfo 机型及其可售机房（用于创建前校验是否可售）。
type ServerTypeInfo struct {
	Name              string
	Cores             int
	MemoryGB          float32
	DiskGB            int
	SellableLocations []string
	// MonthlyGrossEUR 按机房分的月费（EUR，含税价 gross）。
	MonthlyGrossEUR map[string]float64
}

// ActionInfo 一次 Hetzner 异步操作。
type ActionInfo struct {
	ID           int64  `json:"id"`
	Status       string `json:"status"` // running / success / error
	Command      string `json:"command"`
	Progress     int    `json:"progress"`
	ErrorMessage string `json:"error,omitempty"`
}

// CreateServerInput 创建服务器的输入。
type CreateServerInput struct {
	Name          string
	ServerType    string
	Location      string
	Image         ImageRef
	SSHKeys       []int64
	PrimaryIPv4ID *int64
	PrimaryIPv6ID *int64
}

// CreateServerResult 创建结果。
type CreateServerResult struct {
	Server       *ServerInfo
	RootPassword string
	ActionID     int64
}

// DeleteServerOptions 删除服务器的选项。
type DeleteServerOptions struct {
	CreateSnapshot bool
	KeepIPv4       bool
	KeepIPv6       bool
}

// Client 访问 Hetzner Cloud API v1 的最小接口。真实实现走 hcloud-go，
// 测试用 FakeClient。所有方法都不直接读 token，由 Service 在构造时注入。
type Client interface {
	ListServers(ctx context.Context) ([]*ServerInfo, error)
	GetServer(ctx context.Context, id int64) (*ServerInfo, error)
	CreateServer(ctx context.Context, in CreateServerInput) (*CreateServerResult, error)
	DeleteServer(ctx context.Context, id int64) error
	UpdateServerName(ctx context.Context, id int64, name string) (*ServerInfo, error)
	// ServerAction 执行 poweron / poweroff / reboot 等动作。
	ServerAction(ctx context.Context, id int64, action string) (*ActionInfo, error)
	ResetPassword(ctx context.Context, id int64) (string, *ActionInfo, error)
	RebuildServer(ctx context.Context, id int64, image ImageRef) (*ActionInfo, string, error)
	CreateSnapshot(ctx context.Context, serverID int64, description string) (imageID int64, actionID int64, err error)
	ListSnapshots(ctx context.Context) ([]*SnapshotInfo, error)
	DeleteSnapshot(ctx context.Context, id int64) error
	UpdateSnapshot(ctx context.Context, id int64, description string) (*SnapshotInfo, error)
	GetAction(ctx context.Context, id int64) (*ActionInfo, error)
	ListServerTypes(ctx context.Context) ([]*ServerTypeInfo, error)
	// GetOutboundDaily 按 UTC 日期汇总的出流量（metrics network 口径）。
	GetOutboundDaily(ctx context.Context, serverID int64, days int) ([]TrafficPoint, error)
	// GetOutboundTodayBytes 今天 00:00（UTC）至今的出流量。
	GetOutboundTodayBytes(ctx context.Context, serverID int64) (int64, error)
	// GetOutboundMonthBytes 本月 1 日至今的出流量（metrics 口径，用于实时校准）。
	GetOutboundMonthBytes(ctx context.Context, serverID int64) (int64, error)
	SetPrimaryIPAutoDelete(ctx context.Context, id int64, autoDelete bool) error
	UnassignPrimaryIP(ctx context.Context, id int64) (*ActionInfo, error)
	DeletePrimaryIP(ctx context.Context, id int64) error
}
