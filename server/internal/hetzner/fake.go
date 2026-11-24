package hetzner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// FakeClient 内存假实现：单测用，不碰真实 API。
// 调用记录在 Calls 里，便于断言“SAFE_MODE 下没有执行危险动作”。
type FakeClient struct {
	mu sync.Mutex

	Servers   map[int64]*ServerInfo
	Snapshots map[int64]*SnapshotInfo
	Types     []*ServerTypeInfo
	Actions   map[int64]*ActionInfo
	// DailyPoints 按服务器返回的日流量；TodayBytes/MonthBytes 按服务器返回。
	DailyPoints map[int64][]TrafficPoint
	TodayBytes  map[int64]int64
	MonthBytes  map[int64]int64
	// Err 注入：方法名 → 返回的错误。
	Err map[string]error
	// Calls 记录调用："method:id" 或 "method"。
	Calls []string
	// NextIDs 自增 id 分配。
	NextServerID   int64
	NextSnapshotID int64
	NextActionID   int64
	// DeletedPrimaryIPs / UnassignedPrimaryIPs 记录 Primary IP 操作。
	DeletedPrimaryIPs    []int64
	UnassignedPrimaryIPs []int64
	AutoDeleteSet        map[int64]bool
	// CreateFailCount：CreateServer 前 N 次返回 resource_unavailable 风格错误（重试测试用）。
	CreateFailCount int
	// CreatedServers 记录每次 CreateServer 的输入。
	CreatedServers []CreateServerInput
}

func (f *FakeClient) record(call string) {
	f.Calls = append(f.Calls, call)
}

func (f *FakeClient) callErr(method string) error {
	if f.Err == nil {
		return nil
	}
	return f.Err[method]
}

// Called 是否调过某方法（前缀匹配）。
func (f *FakeClient) Called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.Calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func (f *FakeClient) ListServers(_ context.Context) ([]*ServerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListServers")
	if err := f.callErr("ListServers"); err != nil {
		return nil, err
	}
	out := make([]*ServerInfo, 0, len(f.Servers))
	for _, s := range f.Servers {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

func (f *FakeClient) GetServer(_ context.Context, id int64) (*ServerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("GetServer:%d", id))
	if err := f.callErr("GetServer"); err != nil {
		return nil, err
	}
	s, ok := f.Servers[id]
	if !ok {
		return nil, fmt.Errorf("server %d not found", id)
	}
	cp := *s
	return &cp, nil
}

func (f *FakeClient) CreateServer(_ context.Context, in CreateServerInput) (*CreateServerResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CreateServer")
	f.CreatedServers = append(f.CreatedServers, in)
	if f.CreateFailCount > 0 {
		f.CreateFailCount--
		return nil, errors.New("resource_unavailable (412)")
	}
	if err := f.callErr("CreateServer"); err != nil {
		return nil, err
	}
	if f.NextServerID == 0 {
		f.NextServerID = 1000
	}
	f.NextServerID++
	id := f.NextServerID
	srv := &ServerInfo{
		ID: id, Name: in.Name, Status: "running", ServerType: in.ServerType,
		Location: in.Location, Created: time.Now().UTC(),
		IncludedTraffic: uint64(20 * BytesInTB),
	}
	if in.PrimaryIPv4ID != nil {
		srv.IPv4ID = *in.PrimaryIPv4ID
		srv.IPv4 = "203.0.113.1"
	}
	if in.PrimaryIPv6ID != nil {
		srv.IPv6ID = *in.PrimaryIPv6ID
		srv.IPv6 = "2001:db8::1"
	}
	if f.Servers == nil {
		f.Servers = map[int64]*ServerInfo{}
	}
	f.Servers[id] = srv
	return &CreateServerResult{Server: srv, RootPassword: "fake-root-pw"}, nil
}

func (f *FakeClient) DeleteServer(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("DeleteServer:%d", id))
	if err := f.callErr("DeleteServer"); err != nil {
		return err
	}
	if _, ok := f.Servers[id]; !ok {
		return fmt.Errorf("server %d not found", id)
	}
	delete(f.Servers, id)
	return nil
}

func (f *FakeClient) UpdateServerName(_ context.Context, id int64, name string) (*ServerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("UpdateServerName:%d", id))
	if err := f.callErr("UpdateServerName"); err != nil {
		return nil, err
	}
	s, ok := f.Servers[id]
	if !ok {
		return nil, fmt.Errorf("server %d not found", id)
	}
	s.Name = name
	cp := *s
	return &cp, nil
}

func (f *FakeClient) ServerAction(_ context.Context, id int64, action string) (*ActionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("ServerAction:%s:%d", action, id))
	if err := f.callErr("ServerAction"); err != nil {
		return nil, err
	}
	if _, ok := f.Servers[id]; !ok {
		return nil, fmt.Errorf("server %d not found", id)
	}
	f.NextActionID++
	act := &ActionInfo{ID: f.NextActionID, Status: "success", Command: action}
	if f.Actions == nil {
		f.Actions = map[int64]*ActionInfo{}
	}
	f.Actions[act.ID] = act
	return act, nil
}

func (f *FakeClient) ResetPassword(_ context.Context, id int64) (string, *ActionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("ResetPassword:%d", id))
	if err := f.callErr("ResetPassword"); err != nil {
		return "", nil, err
	}
	if _, ok := f.Servers[id]; !ok {
		return "", nil, fmt.Errorf("server %d not found", id)
	}
	return "fake-new-password", &ActionInfo{ID: 1, Status: "success", Command: "reset_password"}, nil
}

func (f *FakeClient) RebuildServer(_ context.Context, id int64, image ImageRef) (*ActionInfo, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("RebuildServer:%d", id))
	if err := f.callErr("RebuildServer"); err != nil {
		return nil, "", err
	}
	if _, ok := f.Servers[id]; !ok {
		return nil, "", fmt.Errorf("server %d not found", id)
	}
	return &ActionInfo{ID: 2, Status: "success", Command: "rebuild"}, "fake-rebuilt-pw", nil
}

func (f *FakeClient) CreateSnapshot(_ context.Context, serverID int64, description string) (int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("CreateSnapshot:%d", serverID))
	if err := f.callErr("CreateSnapshot"); err != nil {
		return 0, 0, err
	}
	if _, ok := f.Servers[serverID]; !ok {
		return 0, 0, fmt.Errorf("server %d not found", serverID)
	}
	if f.NextSnapshotID == 0 {
		f.NextSnapshotID = 5000
	}
	f.NextSnapshotID++
	id := f.NextSnapshotID
	if f.Snapshots == nil {
		f.Snapshots = map[int64]*SnapshotInfo{}
	}
	f.Snapshots[id] = &SnapshotInfo{ID: id, Description: description, SizeGB: 5, Created: time.Now().UTC(), ServerID: serverID}
	f.NextActionID++
	return id, f.NextActionID, nil
}

func (f *FakeClient) ListSnapshots(_ context.Context) ([]*SnapshotInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListSnapshots")
	if err := f.callErr("ListSnapshots"); err != nil {
		return nil, err
	}
	out := make([]*SnapshotInfo, 0, len(f.Snapshots))
	for _, s := range f.Snapshots {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

func (f *FakeClient) DeleteSnapshot(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("DeleteSnapshot:%d", id))
	if err := f.callErr("DeleteSnapshot"); err != nil {
		return err
	}
	if _, ok := f.Snapshots[id]; !ok {
		return fmt.Errorf("snapshot %d not found", id)
	}
	delete(f.Snapshots, id)
	return nil
}

func (f *FakeClient) UpdateSnapshot(_ context.Context, id int64, description string) (*SnapshotInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("UpdateSnapshot:%d", id))
	if err := f.callErr("UpdateSnapshot"); err != nil {
		return nil, err
	}
	s, ok := f.Snapshots[id]
	if !ok {
		return nil, fmt.Errorf("snapshot %d not found", id)
	}
	s.Description = description
	cp := *s
	return &cp, nil
}

func (f *FakeClient) GetAction(_ context.Context, id int64) (*ActionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("GetAction:%d", id))
	if a, ok := f.Actions[id]; ok {
		cp := *a
		return &cp, nil
	}
	// 未登记的 action 默认已成功（快照等待循环用）。
	return &ActionInfo{ID: id, Status: "success", Command: "unknown"}, nil
}

func (f *FakeClient) ListServerTypes(_ context.Context) ([]*ServerTypeInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListServerTypes")
	if err := f.callErr("ListServerTypes"); err != nil {
		return nil, err
	}
	return f.Types, nil
}

func (f *FakeClient) GetOutboundDaily(_ context.Context, serverID int64, _ int) ([]TrafficPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("GetOutboundDaily:%d", serverID))
	if err := f.callErr("GetOutboundDaily"); err != nil {
		return nil, err
	}
	return f.DailyPoints[serverID], nil
}

func (f *FakeClient) GetOutboundTodayBytes(_ context.Context, serverID int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("GetOutboundTodayBytes:%d", serverID))
	if err := f.callErr("GetOutboundTodayBytes"); err != nil {
		return 0, err
	}
	return f.TodayBytes[serverID], nil
}

func (f *FakeClient) GetOutboundMonthBytes(_ context.Context, serverID int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("GetOutboundMonthBytes:%d", serverID))
	if err := f.callErr("GetOutboundMonthBytes"); err != nil {
		return 0, err
	}
	return f.MonthBytes[serverID], nil
}

func (f *FakeClient) SetPrimaryIPAutoDelete(_ context.Context, id int64, autoDelete bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("SetPrimaryIPAutoDelete:%d", id))
	if f.AutoDeleteSet == nil {
		f.AutoDeleteSet = map[int64]bool{}
	}
	f.AutoDeleteSet[id] = autoDelete
	return nil
}

func (f *FakeClient) UnassignPrimaryIP(_ context.Context, id int64) (*ActionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("UnassignPrimaryIP:%d", id))
	f.UnassignedPrimaryIPs = append(f.UnassignedPrimaryIPs, id)
	return &ActionInfo{ID: 3, Status: "success", Command: "unassign"}, nil
}

func (f *FakeClient) DeletePrimaryIP(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("DeletePrimaryIP:%d", id))
	f.DeletedPrimaryIPs = append(f.DeletedPrimaryIPs, id)
	return nil
}
