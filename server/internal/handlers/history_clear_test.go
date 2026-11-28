package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ovh-buy/server/internal/types"
)

// 只删失败记录:成功的和待核对的必须原样保留,落库也要一致。
func TestClearPurchaseHistoryByStatus(t *testing.T) {
	st, _ := newWatchTestMonitor(t)
	seed := []types.PurchaseHistoryEntry{
		{ID: "h1", Status: "failed", PlanCode: "a", PurchaseTime: "2026-11-28T10:00:00Z"},
		{ID: "h2", Status: "failed", PlanCode: "b", PurchaseTime: "2026-11-28T10:01:00Z"},
		{ID: "h3", Status: "success", PlanCode: "c", PurchaseTime: "2026-11-28T10:02:00Z"},
		{ID: "h4", Status: "uncertain", PlanCode: "d", PurchaseTime: "2026-11-28T10:03:00Z"},
	}
	st.HistoryMu.Lock()
	st.History = append([]types.PurchaseHistoryEntry{}, seed...)
	st.HistoryMu.Unlock()
	if err := st.SaveHistory(); err != nil {
		t.Fatalf("seed history: %v", err)
	}

	r := gin.New()
	r.DELETE("/purchase-history", ClearPurchaseHistory(st))

	// 只删 failed
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/purchase-history?status=failed", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=failed 应返回 200,实际 %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		Status  string `json:"status"`
		Deleted int    `json:"deleted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if res.Deleted != 2 {
		t.Fatalf("应删掉 2 条失败记录,实际 deleted=%d", res.Deleted)
	}
	remaining, err := st.DB.ListHistory()
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("应剩下 2 条(成功+待核对),实际 %d 条", len(remaining))
	}
	for _, h := range remaining {
		if h.Status == "failed" {
			t.Fatalf("失败记录 %s 没删掉", h.ID)
		}
	}

	// 非法 status 拒绝,且一条都不动
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/purchase-history?status=bogus", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 status 应返回 400,实际 %d", w.Code)
	}
	if got, _ := st.DB.ListHistory(); len(got) != 2 {
		t.Fatalf("400 之后记录数不应变,实际 %d 条", len(got))
	}

	// 不带 status 还是清空全部(老行为)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/purchase-history", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("清空全部应返回 200,实际 %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.Deleted != 2 {
		t.Fatalf("清空全部应删掉剩下 2 条,实际 %+v (err=%v)", res, err)
	}
	if got, _ := st.DB.ListHistory(); len(got) != 0 {
		t.Fatalf("清空后应剩 0 条,实际 %d 条", len(got))
	}
}

// 只删已过付款期的记录:未过期、已付款/已终态、无订单号、失败的都不能动。
func TestClearPurchaseHistoryExpired(t *testing.T) {
	st, _ := newWatchTestMonitor(t)
	now := time.Now()
	old := now.AddDate(0, 0, -20) // 20 天前下的单,付款窗口 15 天,已过
	recent := now.AddDate(0, 0, -1)
	seed := []types.PurchaseHistoryEntry{
		// 已过期:成功、未付款、无明确到期时间,按下单时间 +15 天算
		{ID: "e1", Status: "success", OrderID: "1001", OrderStatus: "notPaid", PlanCode: "a", PurchaseTime: old.Format(types.NowISOLayout)},
		// 未过期:有明确到期时间,且在未来
		{ID: "e2", Status: "success", OrderID: "1002", OrderStatus: "", PlanCode: "b", PurchaseTime: old.Format(time.RFC3339), ExpirationTime: now.Add(24 * time.Hour).Format(time.RFC3339)},
		// 已付款,不算过期
		{ID: "e3", Status: "success", OrderID: "1003", OrderStatus: "delivered", PlanCode: "c", PurchaseTime: old.Format(types.NowISOLayout)},
		// 还在付款窗口内
		{ID: "e4", Status: "success", OrderID: "1004", OrderStatus: "notPaid", PlanCode: "d", PurchaseTime: recent.Format(types.NowISOLayout)},
		// 失败记录不是"过期"
		{ID: "e5", Status: "failed", PlanCode: "e", PurchaseTime: old.Format(types.NowISOLayout)},
		// 成功但没订单号,不走倒计时
		{ID: "e6", Status: "success", OrderStatus: "notPaid", PlanCode: "f", PurchaseTime: old.Format(types.NowISOLayout)},
		// 已过期:明确到期时间已过(按下单时间算还在窗口内,到期时间说了算)
		{ID: "e7", Status: "success", OrderID: "1007", OrderStatus: "notPaid", PlanCode: "g", PurchaseTime: recent.Format(types.NowISOLayout), ExpirationTime: now.Add(-time.Hour).Format(time.RFC3339)},
	}
	st.HistoryMu.Lock()
	st.History = append([]types.PurchaseHistoryEntry{}, seed...)
	st.HistoryMu.Unlock()
	if err := st.SaveHistory(); err != nil {
		t.Fatalf("seed history: %v", err)
	}

	r := gin.New()
	r.DELETE("/purchase-history", ClearPurchaseHistory(st))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/purchase-history?status=expired", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=expired 应返回 200,实际 %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		Status  string `json:"status"`
		Deleted int    `json:"deleted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if res.Deleted != 2 {
		t.Fatalf("应删掉 2 条过期记录(e1/e7),实际 deleted=%d", res.Deleted)
	}
	remaining, err := st.DB.ListHistory()
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(remaining) != 5 {
		t.Fatalf("应剩下 5 条,实际 %d 条", len(remaining))
	}
	for _, h := range remaining {
		if h.ID == "e1" || h.ID == "e7" {
			t.Fatalf("过期记录 %s 没删掉", h.ID)
		}
	}
}
