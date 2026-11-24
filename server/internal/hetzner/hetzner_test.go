package hetzner

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/ovh-buy/server/internal/db"
	"github.com/ovh-buy/server/internal/logger"
)

func testService(t *testing.T, fake *FakeClient) *Service {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	lg := logger.New(filepath.Join(dir, "t.log"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc := NewService(database, lg, nil)
	svc.NewClient = func(token string) Client { return fake }
	if err := svc.SaveSettings(Settings{Token: "fake-token", TrafficLimitTB: 20, RotateThreshold: 0.98, CheckIntervalMin: 5, SafeMode: true, SnapshotPricePerGB: 0.011}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	return svc
}

func tbServer(id int64, usedTB float64) *ServerInfo {
	return &ServerInfo{
		ID: id, Name: "srv-1", Status: "running", ServerType: "cx22",
		Location: "fsn1", IPv4: "203.0.113.10", IPv4ID: 7, IPv6ID: 8,
		IncludedTraffic: uint64(20 * BytesInTB),
		OutgoingTraffic: uint64(usedTB * float64(BytesInTB)),
		DiskGB:          40,
	}
}

func TestDecidePolicyAction(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name string
		in   PolicyInput
		want string
	}{
		{"safe mode over threshold only alerts", PolicyInput{Enabled: true, Threshold: 0.98, ImageID: "123", UsedTB: 19.8, LimitTB: 20, SafeMode: true, NowUnix: now}, "alert_threshold"},
		{"no safe mode with image rebuilds", PolicyInput{Enabled: true, Threshold: 0.98, ImageID: "123", UsedTB: 19.8, LimitTB: 20, SafeMode: false, NowUnix: now}, "rebuild"},
		{"no safe mode without image alerts", PolicyInput{Enabled: true, Threshold: 0.98, ImageID: "", UsedTB: 19.8, LimitTB: 20, SafeMode: false, NowUnix: now}, "alert_no_image"},
		{"below threshold does nothing", PolicyInput{Enabled: true, Threshold: 0.98, ImageID: "123", UsedTB: 10, LimitTB: 20, SafeMode: false, NowUnix: now}, "none"},
		{"unconfigured over limit powers off", PolicyInput{Enabled: false, UsedTB: 20.5, LimitTB: 20, SafeMode: false, NowUnix: now}, "auto_poweroff"},
		{"unconfigured over limit in safe mode only alerts", PolicyInput{Enabled: false, UsedTB: 20.5, LimitTB: 20, SafeMode: true, NowUnix: now}, "alert_poweroff"},
		{"unconfigured already stopped does not repeat", PolicyInput{Enabled: false, UsedTB: 20.5, LimitTB: 20, SafeMode: false, Guard: GuardEntry{AutoStopped: true}, NowUnix: now}, "none"},
		{"unconfigured near limit warns", PolicyInput{Enabled: false, UsedTB: 19.5, LimitTB: 20, SafeMode: true, NowUnix: now}, "warn"},
		{"near limit warn throttled within 10m", PolicyInput{Enabled: false, UsedTB: 19.5, LimitTB: 20, SafeMode: true, Guard: GuardEntry{LastWarnTS: now - 60}, NowUnix: now}, "none"},
		{"low usage does nothing", PolicyInput{Enabled: false, UsedTB: 5, LimitTB: 20, SafeMode: true, NowUnix: now}, "none"},
	}
	for _, c := range cases {
		if got := DecidePolicyAction(c.in).Action; got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestValidateSettings(t *testing.T) {
	if err := ValidateSettings(DefaultSettings()); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
	for _, bad := range []Settings{
		{TrafficLimitTB: 0, RotateThreshold: 0.98, CheckIntervalMin: 5},
		{TrafficLimitTB: 20, RotateThreshold: 0, CheckIntervalMin: 5},
		{TrafficLimitTB: 20, RotateThreshold: 1.5, CheckIntervalMin: 5},
		{TrafficLimitTB: 20, RotateThreshold: 0.98, CheckIntervalMin: 0},
		{TrafficLimitTB: 20, RotateThreshold: 0.98, CheckIntervalMin: 2000},
	} {
		if err := ValidateSettings(bad); err == nil {
			t.Errorf("expected validation error for %+v", bad)
		}
	}
}

func TestRolloverMonthReset(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	store := NewSettingsStore(database)
	if err := store.AddRollover("2026-11-24", "2026-11", 100, 50); err != nil {
		t.Fatalf("add rollover: %v", err)
	}
	got := store.RolloverTotals("2026-11-24", "2026-11")
	if got.MonthBytes != 100 || got.DayBytes != 50 {
		t.Fatalf("same month/day totals = %+v", got)
	}
	// 跨月清零，跨日只清日累计。
	if got := store.RolloverTotals("2026-12-01", "2026-12"); got.MonthBytes != 0 || got.DayBytes != 0 {
		t.Fatalf("new month totals should reset: %+v", got)
	}
	if got := store.RolloverTotals("2026-11-25", "2026-11"); got.MonthBytes != 100 || got.DayBytes != 0 {
		t.Fatalf("new day keeps month, resets day: %+v", got)
	}
}

func TestCollectServersTraffic(t *testing.T) {
	fake := &FakeClient{Servers: map[int64]*ServerInfo{1: tbServer(1, 19)}}
	svc := testService(t, fake)
	rows, err := svc.CollectServers(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Traffic.Percent < 0.94 || rows[0].Traffic.Percent > 0.96 {
		t.Errorf("percent = %v, want ~0.95", rows[0].Traffic.Percent)
	}
	if rows[0].Traffic.LimitBytes != uint64(20*BytesInTB) {
		t.Errorf("limit = %d", rows[0].Traffic.LimitBytes)
	}
}

func TestEvaluatePoliciesSafeModeOnlyAlerts(t *testing.T) {
	fake := &FakeClient{Servers: map[int64]*ServerInfo{1: tbServer(1, 19.8)}}
	svc := testService(t, fake)
	if _, err := svc.SetPolicy(1, true, 0.98, "123"); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	res, err := svc.EvaluatePolicies(context.Background())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(res.Actions) != 1 || res.Actions[0].Action != "alert_threshold" {
		t.Fatalf("actions = %+v", res.Actions)
	}
	if fake.Called("DeleteServer") || fake.Called("CreateServer") {
		t.Errorf("SAFE_MODE must not execute destructive actions, calls = %v", fake.Calls)
	}
}

func TestEvaluatePoliciesRebuildWhenSafeModeOff(t *testing.T) {
	fake := &FakeClient{Servers: map[int64]*ServerInfo{1: tbServer(1, 19.8)}}
	svc := testService(t, fake)
	st := svc.Settings()
	st.SafeMode = false
	if err := svc.SaveSettings(st); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if _, err := svc.SetPolicy(1, true, 0.98, "123"); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	res, err := svc.EvaluatePolicies(context.Background())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(res.Actions) != 1 || res.Actions[0].Action != "rebuild" {
		t.Fatalf("actions = %+v", res.Actions)
	}
	if !fake.Called("DeleteServer") || !fake.Called("CreateServer") {
		t.Errorf("rebuild should delete old and create new, calls = %v", fake.Calls)
	}
}

func TestRebuildKeepsPrimaryIPs(t *testing.T) {
	fake := &FakeClient{Servers: map[int64]*ServerInfo{1: tbServer(1, 5)}}
	svc := testService(t, fake)
	out, err := svc.Rebuild(context.Background(), 1, ParseImageRef("123"))
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if len(fake.CreatedServers) != 1 {
		t.Fatalf("created = %d", len(fake.CreatedServers))
	}
	created := fake.CreatedServers[0]
	if created.PrimaryIPv4ID == nil || *created.PrimaryIPv4ID != 7 {
		t.Errorf("rebuild must reuse original IPv4 id 7, got %+v", created.PrimaryIPv4ID)
	}
	if created.PrimaryIPv6ID == nil || *created.PrimaryIPv6ID != 8 {
		t.Errorf("rebuild must reuse original IPv6 id 8, got %+v", created.PrimaryIPv6ID)
	}
	if _, ok := fake.Servers[1]; ok {
		t.Errorf("old server should be deleted")
	}
	if out["new_server_id"] == nil {
		t.Errorf("result missing new_server_id: %v", out)
	}
}

func TestDeleteServerKeepIP(t *testing.T) {
	fake := &FakeClient{Servers: map[int64]*ServerInfo{1: tbServer(1, 5)}}
	svc := testService(t, fake)
	if _, err := svc.DeleteServer(context.Background(), 1, DeleteServerOptions{KeepIPv4: true}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !fake.Called("UnassignPrimaryIP:7") {
		t.Errorf("keeping IPv4 must unassign it first, calls = %v", fake.Calls)
	}
	for _, id := range fake.DeletedPrimaryIPs {
		if id == 7 {
			t.Errorf("kept IPv4 must not be deleted")
		}
	}
	if len(fake.DeletedPrimaryIPs) == 0 {
		t.Errorf("IPv6 (not kept) should be deleted, deleted = %v", fake.DeletedPrimaryIPs)
	}
}

func TestSnapshotValidation(t *testing.T) {
	fake := &FakeClient{Servers: map[int64]*ServerInfo{1: tbServer(1, 1)}}
	svc := testService(t, fake)
	if _, err := svc.RenameSnapshotManual(context.Background(), 1, "  "); err == nil {
		t.Errorf("empty snapshot description should fail")
	}
	if _, err := svc.SetPolicy(1, true, 0, "123"); err == nil {
		t.Errorf("threshold 0 should fail")
	}
	if _, err := svc.SetPolicy(1, true, 1.2, "123"); err == nil {
		t.Errorf("threshold > 1 should fail")
	}
	if _, err := svc.RenameServer(context.Background(), 1, ""); err == nil {
		t.Errorf("empty server name should fail")
	}
}

func TestEstimateSnapshotNoHistory(t *testing.T) {
	fake := &FakeClient{Servers: map[int64]*ServerInfo{1: tbServer(1, 1)}}
	svc := testService(t, fake)
	est, err := svc.EstimateSnapshot(context.Background(), 1)
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	// 无历史：磁盘 40GB × 35% = 14GB；月费 14 × 0.011 = 0.154 EUR。
	if est.EstimatedSizeGB != 14 {
		t.Errorf("estimated size = %v, want 14", est.EstimatedSizeGB)
	}
	if est.EstimatedMonthlyEUR != 0.154 {
		t.Errorf("monthly = %v, want 0.154", est.EstimatedMonthlyEUR)
	}
}

func TestDailyTrafficPersists(t *testing.T) {
	fake := &FakeClient{
		Servers:     map[int64]*ServerInfo{1: tbServer(1, 1)},
		DailyPoints: map[int64][]TrafficPoint{1: {{Date: "2026-11-23", Bytes: 12345}}},
	}
	svc := testService(t, fake)
	points, _, err := svc.DailyTraffic(context.Background(), 1, 7)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if len(points) != 1 || points[0].Bytes != 12345 {
		t.Fatalf("points = %+v", points)
	}
	rows, err := svc.database.ListHetznerDailyStats(1, 7)
	if err != nil {
		t.Fatalf("list daily stats: %v", err)
	}
	if len(rows) != 1 || rows[0].Bytes != 12345 {
		t.Fatalf("persisted rows = %+v", rows)
	}
}

func TestClientRequiresToken(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	lg := logger.New(filepath.Join(dir, "t.log"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc := NewService(database, lg, nil)
	if _, err := svc.Client(); err == nil {
		t.Errorf("expected ErrNoToken without a token")
	}
}
