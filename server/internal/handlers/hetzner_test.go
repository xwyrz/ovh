package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ovh-buy/server/internal/app"
	"github.com/ovh-buy/server/internal/config"
	"github.com/ovh-buy/server/internal/db"
	"github.com/ovh-buy/server/internal/hetzner"
	"github.com/ovh-buy/server/internal/logger"
	"github.com/ovh-buy/server/internal/secret"
	"github.com/ovh-buy/server/internal/storage"
)

func newHZTestState(t *testing.T, fake *hetzner.FakeClient, withToken bool) *app.State {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	database, err := db.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	lg := logger.New(filepath.Join(dir, "t.log"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	state := app.NewState(storage.Paths{DataDir: dir}, config.New(database), lg, database)
	svc := hetzner.NewService(database, lg, nil)
	svc.NewClient = func(token string) hetzner.Client { return fake }
	state.Hetzner = svc
	if withToken {
		if err := svc.SaveSettings(hetzner.Settings{
			Token: "fake-token", TrafficLimitTB: 20, RotateThreshold: 0.98,
			CheckIntervalMin: 5, SafeMode: true, SnapshotPricePerGB: 0.011,
		}); err != nil {
			t.Fatalf("save settings: %v", err)
		}
	}
	return state
}

func hzCall(t *testing.T, state *app.State, h gin.HandlerFunc, method, path, body string, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	c.Request = httptest.NewRequest(method, path, reader)
	if body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	for k, v := range params {
		c.Params = append(c.Params, gin.Param{Key: k, Value: v})
	}
	h(c)
	return w
}

func hzDecode(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return out
}

func TestHetznerStatusWithoutToken(t *testing.T) {
	state := newHZTestState(t, &hetzner.FakeClient{}, false)
	w := hzCall(t, state, GetHetznerStatus(state), "GET", "/api/hetzner/status", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	out := hzDecode(t, w)
	if out["configured"] != false {
		t.Errorf("configured = %v", out["configured"])
	}
	if strings.Contains(w.Body.String(), "fake-token") {
		t.Errorf("status must never leak the token")
	}
	// 需要 token 的端点返回 400，而不是 500。
	w = hzCall(t, state, ListHetznerServers(state), "GET", "/api/hetzner/servers", "", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("servers without token = %d, want 400", w.Code)
	}
	out = hzDecode(t, w)
	if out["code"] != "hetzner_token_missing" {
		t.Errorf("code = %v", out["code"])
	}
}

func TestHetznerSettingsRoundTrip(t *testing.T) {
	state := newHZTestState(t, &hetzner.FakeClient{}, false)
	// 非法阈值拒绝。
	w := hzCall(t, state, PutHetznerSettings(state), "PUT", "/api/hetzner/settings",
		`{"hetzner_token":"tok-123","traffic_limit_tb":20,"rotate_threshold":1.5,"check_interval_minutes":5,"safe_mode":true}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid threshold = %d, want 400", w.Code)
	}
	// 合法保存；GET 不返回 token 明文。
	w = hzCall(t, state, PutHetznerSettings(state), "PUT", "/api/hetzner/settings",
		`{"hetzner_token":"tok-123","traffic_limit_tb":10,"rotate_threshold":0.9,"check_interval_minutes":5,"safe_mode":true}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("put settings = %d: %s", w.Code, w.Body.String())
	}
	w = hzCall(t, state, GetHetznerSettings(state), "GET", "/api/hetzner/settings", "", nil)
	if strings.Contains(w.Body.String(), "tok-123") {
		t.Errorf("settings GET must not return the token明文: %s", w.Body.String())
	}
	out := hzDecode(t, w)
	if out["configured"] != true || out["traffic_limit_tb"] != float64(10) {
		t.Errorf("settings = %v", out)
	}
	// 空 token 的 PUT 不改原 token（之后仍 configured）。
	w = hzCall(t, state, PutHetznerSettings(state), "PUT", "/api/hetzner/settings",
		`{"hetzner_token":"","traffic_limit_tb":10,"rotate_threshold":0.9,"check_interval_minutes":5,"safe_mode":true}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("put empty token = %d", w.Code)
	}
	w = hzCall(t, state, GetHetznerSettings(state), "GET", "/api/hetzner/settings", "", nil)
	if hzDecode(t, w)["configured"] != true {
		t.Errorf("empty token PUT must keep the saved token")
	}
}

func TestHetznerPoliciesCRUD(t *testing.T) {
	state := newHZTestState(t, &hetzner.FakeClient{}, false)
	w := hzCall(t, state, CreateHetznerPolicy(state), "POST", "/api/hetzner/policies",
		`{"server_id":42,"enabled":true,"threshold":0.9,"image_id":"777"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("create policy = %d: %s", w.Code, w.Body.String())
	}
	w = hzCall(t, state, ListHetznerPolicies(state), "GET", "/api/hetzner/policies", "", nil)
	if !strings.Contains(w.Body.String(), "42") {
		t.Errorf("list should contain server 42: %s", w.Body.String())
	}
	w = hzCall(t, state, DeleteHetznerPolicy(state), "DELETE", "/api/hetzner/policies/42", "", map[string]string{"server_id": "42"})
	if w.Code != http.StatusOK {
		t.Fatalf("delete policy = %d", w.Code)
	}
	w = hzCall(t, state, ListHetznerPolicies(state), "GET", "/api/hetzner/policies", "", nil)
	if strings.Contains(w.Body.String(), "42") {
		t.Errorf("policy should be gone: %s", w.Body.String())
	}
}

func TestHetznerCreateServerJobPolling(t *testing.T) {
	fake := &hetzner.FakeClient{
		Types: []*hetzner.ServerTypeInfo{{Name: "cx22", SellableLocations: []string{"fsn1"}}},
	}
	state := newHZTestState(t, fake, true)
	w := hzCall(t, state, CreateHetznerServer(state), "POST", "/api/hetzner/servers",
		`{"name":"test-1","server_type":"cx22","location":"fsn1","image":"debian-12"}`, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	jobID := hzDecode(t, w)["job_id"].(string)
	if jobID == "" {
		t.Fatalf("no job_id: %s", w.Body.String())
	}
	// 轮询 job 直到 success。
	deadline := time.Now().Add(5 * time.Second)
	for {
		w = hzCall(t, state, GetHetznerAction(state), "GET", "/api/hetzner/actions/"+jobID, "", map[string]string{"id": jobID})
		if w.Code != http.StatusOK {
			t.Fatalf("get action = %d", w.Code)
		}
		if hzDecode(t, w)["status"] == "success" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not succeed: %s", w.Body.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(fake.Servers) != 1 {
		t.Errorf("fake servers = %d, want 1", len(fake.Servers))
	}
}

func TestHetznerCheckSafeMode(t *testing.T) {
	tb := float64(hetzner.BytesInTB)
	fake := &hetzner.FakeClient{Servers: map[int64]*hetzner.ServerInfo{
		1: {ID: 1, Name: "srv-1", Status: "running", ServerType: "cx22", Location: "fsn1",
			IncludedTraffic: uint64(20 * hetzner.BytesInTB),
			OutgoingTraffic: uint64(19.8 * tb)},
	}}
	state := newHZTestState(t, fake, true)
	if _, err := state.Hetzner.SetPolicy(1, true, 0.98, "123"); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	w := hzCall(t, state, CheckHetzner(state), "POST", "/api/hetzner/check", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("check = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "alert_threshold") {
		t.Errorf("safe mode check should alert, got %s", w.Body.String())
	}
	if fake.Called("DeleteServer") {
		t.Errorf("safe mode check must not delete anything")
	}
}

func TestHetznerQBNodeNoPasswordLeak(t *testing.T) {
	// 生产环境 main 会在开库前 secret.Init；单测里按需初始化一次，
	// 否则 secret.Encrypt 会退化成明文返回（见 secret 包注释）。
	if !secret.Enabled() {
		if err := secret.Init(t.TempDir(), filepath.Join(t.TempDir(), ".env")); err != nil {
			t.Fatalf("secret init: %v", err)
		}
	}
	state := newHZTestState(t, &hetzner.FakeClient{}, false)
	// QB 节点保存时不测通（QBFetch 会连真实地址）——注入一个直接失败的 fetch，
	// 验证节点仍保存、列表不返回密码。
	state.Hetzner.QBFetch = func(_ context.Context, _, _, _ string) (hetzner.QBStats, error) {
		return hetzner.QBStats{Enabled: true, Error: "unreachable"}, nil
	}
	w := hzCall(t, state, CreateHetznerQBNode(state), "POST", "/api/hetzner/qb/nodes",
		`{"server_id":9,"url":"http://127.0.0.1:8080","username":"admin","password":"secret-pw"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("create qb node = %d: %s", w.Code, w.Body.String())
	}
	w = hzCall(t, state, ListHetznerQBNodes(state), "GET", "/api/hetzner/qb/nodes", "", nil)
	if strings.Contains(w.Body.String(), "secret-pw") {
		t.Errorf("qb node list must not leak the password: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"server_id":9`) {
		t.Errorf("qb node list should contain server 9: %s", w.Body.String())
	}
	// DB 里存的是密文。
	nodes, err := state.DB.ListHetznerQBNodes()
	if err != nil || len(nodes) != 1 {
		t.Fatalf("db qb nodes = %v, %v", nodes, err)
	}
	raw := ""
	_ = state.DB.Get(&raw, `SELECT password FROM hetzner_qb_nodes WHERE server_id = 9`)
	if strings.Contains(raw, "secret-pw") {
		t.Errorf("qb password must be encrypted at rest")
	}
}
