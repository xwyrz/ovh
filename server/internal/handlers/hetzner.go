package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ovh-buy/server/internal/app"
	"github.com/ovh-buy/server/internal/db"
	"github.com/ovh-buy/server/internal/hetzner"
)

// Hetzner 路由（/api/hetzner），风格照现有 handlers：
// func X(state *app.State) gin.HandlerFunc，错误响应 {"status":"error","message":...}。
// 未配 token 时返回 400（英文 message + 中文说明），不 panic、不当 500。

func hzErr(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{"status": "error", "message": err.Error()})
}

// hzService 取 Hetzner 服务；未初始化（单测里没接线）时当未配置处理。
func hzService(state *app.State) (*hetzner.Service, error) {
	if state == nil || state.Hetzner == nil {
		return nil, hetzner.ErrNoToken
	}
	return state.Hetzner, nil
}

// hzRequireToken 需要 token 的端点共用的前置检查。
func hzRequireToken(state *app.State) (*hetzner.Service, bool) {
	svc, err := hzService(state)
	if err != nil {
		return nil, false
	}
	if !svc.Configured() {
		return nil, false
	}
	return svc, true
}

func hzNoToken(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{
		"status":  "error",
		"message": "Hetzner token is not configured (Hetzner Token 未配置，请先在设置里填写)",
		"code":    "hetzner_token_missing",
	})
}

func hzParseID(c *gin.Context, param string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(param), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "invalid id: must be a positive integer"})
		return 0, false
	}
	return id, true
}

// hzImageField image 字段可能是字符串（名字）或数字（id）。
func hzImageField(v interface{}) hetzner.ImageRef {
	switch t := v.(type) {
	case nil:
		return hetzner.ImageRef{}
	case string:
		return hetzner.ParseImageRef(t)
	case float64:
		return hetzner.ImageRef{ID: int64(t)}
	case int64:
		return hetzner.ImageRef{ID: t}
	default:
		return hetzner.ImageRef{}
	}
}

func hzCtx(c *gin.Context) context.Context {
	return c.Request.Context()
}

// GetHetznerStatus GET /api/hetzner/status（不需 token，不返回 token）。
func GetHetznerStatus(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, err := hzService(state)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"configured": false, "safe_mode": true,
				"traffic_limit_tb":       hetzner.DefaultTrafficLimitTB,
				"rotate_threshold":       hetzner.DefaultRotateThreshold,
				"check_interval_minutes": hetzner.DefaultCheckIntervalMin,
				"loop_running":           false,
			})
			return
		}
		st := svc.Settings()
		c.JSON(http.StatusOK, gin.H{
			"configured":             svc.Configured(),
			"safe_mode":              st.SafeMode,
			"traffic_limit_tb":       st.TrafficLimitTB,
			"rotate_threshold":       st.RotateThreshold,
			"check_interval_minutes": st.CheckIntervalMin,
			"loop_running":           svc.LoopRunning(),
		})
	}
}

// ListHetznerServers GET /api/hetzner/servers。
func ListHetznerServers(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		rows, err := svc.CollectServers(hzCtx(c))
		if err != nil {
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		if rows == nil {
			rows = []hetzner.ServerRow{}
		}
		roll := svc.RolloverTotals()
		c.JSON(http.StatusOK, gin.H{
			"servers": rows,
			"rollover": gin.H{
				"month": roll.Month, "month_bytes": roll.MonthBytes,
				"day": roll.Day, "day_bytes": roll.DayBytes,
			},
		})
	}
}

// GetHetznerServer GET /api/hetzner/servers/:id。
func GetHetznerServer(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "id")
		if !ok {
			return
		}
		srv, err := svc.GetServer(hzCtx(c), id)
		if err != nil {
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		st := svc.Settings()
		limit := srv.IncludedTraffic
		if limit == 0 {
			limit = uint64(st.TrafficLimitTB * float64(hetzner.BytesInTB))
		}
		percent := 0.0
		if limit > 0 {
			percent = float64(srv.OutgoingTraffic) / float64(limit)
		}
		c.JSON(http.StatusOK, gin.H{
			"id": srv.ID, "name": srv.Name, "status": srv.Status,
			"server_type": srv.ServerType, "location": srv.Location,
			"ipv4": srv.IPv4, "ipv6": srv.IPv6,
			"created": srv.Created.Format(time.RFC3339),
			"traffic": gin.H{
				"used_bytes": srv.OutgoingTraffic, "limit_bytes": limit, "percent": percent,
			},
		})
	}
}

// GetHetznerServerTraffic GET /api/hetzner/servers/:id/traffic?days=30。
func GetHetznerServerTraffic(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "id")
		if !ok {
			return
		}
		days := 30
		if d, err := strconv.Atoi(c.DefaultQuery("days", "30")); err == nil && d > 0 {
			days = d
		}
		points, archived, err := svc.DailyTraffic(hzCtx(c), id, days)
		if err != nil {
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		if points == nil {
			points = []hetzner.TrafficPoint{}
		}
		if archived == nil {
			archived = []hetzner.TrafficPoint{}
		}
		c.JSON(http.StatusOK, gin.H{
			"server_id": id, "days": days,
			"daily": points, "archived": archived,
		})
	}
}

// CreateHetznerServer POST /api/hetzner/servers（后台 job，返回 job id 轮询）。
func CreateHetznerServer(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		var req struct {
			Name          string      `json:"name"`
			ServerType    string      `json:"server_type"`
			Location      string      `json:"location"`
			Image         interface{} `json:"image"`
			SSHKeys       []int64     `json:"ssh_keys"`
			PrimaryIPv4ID *int64      `json:"primary_ipv4_id"`
			PrimaryIPv6ID *int64      `json:"primary_ipv6_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			hzErr(c, http.StatusBadRequest, err)
			return
		}
		in := hetzner.CreateServerInput{
			Name: strings.TrimSpace(req.Name), ServerType: strings.TrimSpace(req.ServerType),
			Location: strings.TrimSpace(req.Location), Image: hzImageField(req.Image),
			SSHKeys: req.SSHKeys, PrimaryIPv4ID: req.PrimaryIPv4ID, PrimaryIPv6ID: req.PrimaryIPv6ID,
		}
		if in.Name == "" || in.ServerType == "" || in.Image.IsZero() {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "name, server_type and image are required"})
			return
		}
		jobID := svc.RunJob("create_server", func() (interface{}, error) {
			return svc.CreateServer(context.Background(), in)
		})
		c.JSON(http.StatusAccepted, gin.H{"status": "queued", "job_id": jobID, "message": "create started in background"})
	}
}

// RebuildHetznerServer POST /api/hetzner/servers/:id/rebuild（保留原 IP，后台 job）。
func RebuildHetznerServer(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "id")
		if !ok {
			return
		}
		var req struct {
			ImageID interface{} `json:"image_id"`
		}
		_ = c.ShouldBindJSON(&req)
		image := hzImageField(req.ImageID)
		jobID := svc.RunJob("rebuild", func() (interface{}, error) {
			return svc.Rebuild(context.Background(), id, image)
		})
		c.JSON(http.StatusAccepted, gin.H{"status": "queued", "job_id": jobID, "message": "rebuild started in background"})
	}
}

// RebuildFullHetznerServer POST /api/hetzner/servers/:id/rebuild-full（先建新机再删旧机，换 IP）。
func RebuildFullHetznerServer(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "id")
		if !ok {
			return
		}
		var req struct {
			ImageID interface{} `json:"image_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			hzErr(c, http.StatusBadRequest, err)
			return
		}
		image := hzImageField(req.ImageID)
		if image.IsZero() {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "image_id is required for full rebuild"})
			return
		}
		jobID := svc.RunJob("rebuild_full", func() (interface{}, error) {
			return svc.RebuildFull(context.Background(), id, image)
		})
		c.JSON(http.StatusAccepted, gin.H{"status": "queued", "job_id": jobID, "message": "full rebuild started in background"})
	}
}

// RebootHetznerServer POST /api/hetzner/servers/:id/reboot。
func RebootHetznerServer(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "id")
		if !ok {
			return
		}
		act, err := svc.Reboot(hzCtx(c), id)
		if err != nil {
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "server_id": id, "action": act})
	}
}

// HardRebootHetznerServer POST /api/hetzner/servers/:id/hard-reboot。
func HardRebootHetznerServer(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "id")
		if !ok {
			return
		}
		res, err := svc.HardReboot(hzCtx(c), id)
		if err != nil {
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "result": res})
	}
}

// ResetHetznerPassword POST /api/hetzner/servers/:id/reset-password。
// 新密码只在本次响应里返回一次。
func ResetHetznerPassword(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "id")
		if !ok {
			return
		}
		pw, err := svc.ResetPassword(hzCtx(c), id)
		if err != nil {
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "server_id": id, "new_password": pw})
	}
}

// RenameHetznerServer PATCH /api/hetzner/servers/:id。
func RenameHetznerServer(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "id")
		if !ok {
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			hzErr(c, http.StatusBadRequest, err)
			return
		}
		updated, err := svc.RenameServer(hzCtx(c), id, req.Name)
		if err != nil {
			if strings.Contains(err.Error(), "name is required") {
				hzErr(c, http.StatusBadRequest, err)
				return
			}
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "server_id": id, "name": updated.Name})
	}
}

// DeleteHetznerServer DELETE /api/hetzner/servers/:id（后台 job）。
func DeleteHetznerServer(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "id")
		if !ok {
			return
		}
		var req struct {
			CreateSnapshot bool `json:"create_snapshot"`
			KeepIPv4       bool `json:"keep_ipv4"`
			KeepIPv6       bool `json:"keep_ipv6"`
		}
		_ = c.ShouldBindJSON(&req)
		opts := hetzner.DeleteServerOptions{
			CreateSnapshot: req.CreateSnapshot, KeepIPv4: req.KeepIPv4, KeepIPv6: req.KeepIPv6,
		}
		jobID := svc.RunJob("delete_server", func() (interface{}, error) {
			return svc.DeleteServer(context.Background(), id, opts)
		})
		c.JSON(http.StatusAccepted, gin.H{"status": "queued", "job_id": jobID, "message": "delete started in background"})
	}
}

// ListHetznerSnapshots GET /api/hetzner/snapshots。
func ListHetznerSnapshots(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		snaps, err := svc.ListSnapshots(hzCtx(c))
		if err != nil {
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		if snaps == nil {
			snaps = []hetzner.SnapshotInfo{}
		}
		c.JSON(http.StatusOK, gin.H{"snapshots": snaps})
	}
}

// CreateHetznerSnapshot POST /api/hetzner/snapshots。
func CreateHetznerSnapshot(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		var req struct {
			ServerID    int64  `json:"server_id"`
			Description string `json:"description"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			hzErr(c, http.StatusBadRequest, err)
			return
		}
		if req.ServerID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "server_id is required"})
			return
		}
		res, err := svc.CreateSnapshotManual(hzCtx(c), req.ServerID, req.Description)
		if err != nil {
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "result": res})
	}
}

// DeleteHetznerSnapshot DELETE /api/hetzner/snapshots/:id。
func DeleteHetznerSnapshot(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "id")
		if !ok {
			return
		}
		if err := svc.DeleteSnapshotManual(hzCtx(c), id); err != nil {
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "deleted": id})
	}
}

// RenameHetznerSnapshot PATCH /api/hetzner/snapshots/:id。
func RenameHetznerSnapshot(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "id")
		if !ok {
			return
		}
		var req struct {
			Description string `json:"description"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			hzErr(c, http.StatusBadRequest, err)
			return
		}
		sn, err := svc.RenameSnapshotManual(hzCtx(c), id, req.Description)
		if err != nil {
			if strings.Contains(err.Error(), "description is required") {
				hzErr(c, http.StatusBadRequest, err)
				return
			}
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "snapshot": sn})
	}
}

// EstimateHetznerSnapshot GET /api/hetzner/snapshots/:server_id/estimate。
func EstimateHetznerSnapshot(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "server_id")
		if !ok {
			return
		}
		est, err := svc.EstimateSnapshot(hzCtx(c), id)
		if err != nil {
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		c.JSON(http.StatusOK, est)
	}
}

// ListHetznerPolicies GET /api/hetzner/policies（本地配置，不需 token）。
func ListHetznerPolicies(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, err := hzService(state)
		if err != nil {
			hzNoToken(c)
			return
		}
		policies, err := svc.ListPolicies()
		if err != nil {
			hzErr(c, http.StatusInternalServerError, err)
			return
		}
		if policies == nil {
			policies = []db.HetznerPolicy{}
		}
		c.JSON(http.StatusOK, gin.H{"policies": policies})
	}
}

// CreateHetznerPolicy POST /api/hetzner/policies。
func CreateHetznerPolicy(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, err := hzService(state)
		if err != nil {
			hzNoToken(c)
			return
		}
		var req struct {
			ServerID  int64       `json:"server_id"`
			Enabled   bool        `json:"enabled"`
			Threshold float64     `json:"threshold"`
			ImageID   interface{} `json:"image_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			hzErr(c, http.StatusBadRequest, err)
			return
		}
		if req.ServerID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "server_id is required"})
			return
		}
		if req.Threshold == 0 {
			req.Threshold = hetzner.DefaultRotateThreshold
		}
		pol, err := svc.SetPolicy(req.ServerID, req.Enabled, req.Threshold, hzImageField(req.ImageID).String())
		if err != nil {
			hzErr(c, http.StatusBadRequest, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "policy": pol})
	}
}

// DeleteHetznerPolicy DELETE /api/hetzner/policies/:server_id。
func DeleteHetznerPolicy(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, err := hzService(state)
		if err != nil {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "server_id")
		if !ok {
			return
		}
		if err := svc.DeletePolicy(id); err != nil {
			hzErr(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "server_id": id})
	}
}

// GetHetznerSettings GET /api/hetzner/settings（不返回 token）。
func GetHetznerSettings(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, err := hzService(state)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"configured": false, "safe_mode": true,
				"traffic_limit_tb":       hetzner.DefaultTrafficLimitTB,
				"rotate_threshold":       hetzner.DefaultRotateThreshold,
				"check_interval_minutes": hetzner.DefaultCheckIntervalMin,
			})
			return
		}
		st := svc.Settings()
		c.JSON(http.StatusOK, gin.H{
			"configured":             svc.Configured(),
			"traffic_limit_tb":       st.TrafficLimitTB,
			"rotate_threshold":       st.RotateThreshold,
			"check_interval_minutes": st.CheckIntervalMin,
			"safe_mode":              st.SafeMode,
			"snapshot_price_per_gb":  st.SnapshotPricePerGB,
		})
	}
}

// PutHetznerSettings PUT /api/hetzner/settings（token 为空表示不改）。
func PutHetznerSettings(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, err := hzService(state)
		if err != nil {
			hzNoToken(c)
			return
		}
		var req struct {
			Token              *string  `json:"hetzner_token"`
			TrafficLimitTB     *float64 `json:"traffic_limit_tb"`
			RotateThreshold    *float64 `json:"rotate_threshold"`
			CheckIntervalMin   *int     `json:"check_interval_minutes"`
			SafeMode           *bool    `json:"safe_mode"`
			SnapshotPricePerGB *float64 `json:"snapshot_price_per_gb"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			hzErr(c, http.StatusBadRequest, err)
			return
		}
		st := svc.Settings()
		if req.Token != nil {
			st.Token = strings.TrimSpace(*req.Token)
		}
		if req.TrafficLimitTB != nil {
			st.TrafficLimitTB = *req.TrafficLimitTB
		}
		if req.RotateThreshold != nil {
			st.RotateThreshold = *req.RotateThreshold
		}
		if req.CheckIntervalMin != nil {
			st.CheckIntervalMin = *req.CheckIntervalMin
		}
		if req.SafeMode != nil {
			st.SafeMode = *req.SafeMode
		}
		if req.SnapshotPricePerGB != nil {
			st.SnapshotPricePerGB = *req.SnapshotPricePerGB
		}
		if err := svc.SaveSettings(st); err != nil {
			hzErr(c, http.StatusBadRequest, err)
			return
		}
		// token / 间隔变了就重启后台循环。
		svc.RestartLoop()
		c.JSON(http.StatusOK, gin.H{"status": "success", "configured": svc.Configured()})
	}
}

// CheckHetzner POST /api/hetzner/check（手动触发一次流量检查与策略评估）。
func CheckHetzner(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		res, err := svc.EvaluatePolicies(hzCtx(c))
		if err != nil {
			if errors.Is(err, hetzner.ErrNoToken) {
				hzNoToken(c)
				return
			}
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

// ListHetznerQBNodes GET /api/hetzner/qb/nodes（不返回密码）。
func ListHetznerQBNodes(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, err := hzService(state)
		if err != nil {
			hzNoToken(c)
			return
		}
		nodes, err := svc.ListQBNodes()
		if err != nil {
			hzErr(c, http.StatusInternalServerError, err)
			return
		}
		out := make([]gin.H, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, gin.H{"server_id": n.ServerID, "url": n.URL, "username": n.Username})
		}
		c.JSON(http.StatusOK, gin.H{"nodes": out})
	}
}

// CreateHetznerQBNode POST /api/hetzner/qb/nodes。
func CreateHetznerQBNode(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, err := hzService(state)
		if err != nil {
			hzNoToken(c)
			return
		}
		var req struct {
			ServerID int64  `json:"server_id"`
			URL      string `json:"url"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			hzErr(c, http.StatusBadRequest, err)
			return
		}
		if req.ServerID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "server_id is required"})
			return
		}
		stats, ferr := svc.SetQBNode(hzCtx(c), req.ServerID, req.URL, req.Username, req.Password)
		if ferr != nil {
			// 节点已保存，但连通性测试失败：如实返回，不当成整体失败。
			c.JSON(http.StatusOK, gin.H{
				"status": "success", "server_id": req.ServerID,
				"saved": true, "qb_error": ferr.Error(), "qb_status": stats,
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "server_id": req.ServerID, "qb_status": stats})
	}
}

// DeleteHetznerQBNode DELETE /api/hetzner/qb/nodes/:server_id。
func DeleteHetznerQBNode(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, err := hzService(state)
		if err != nil {
			hzNoToken(c)
			return
		}
		id, ok := hzParseID(c, "server_id")
		if !ok {
			return
		}
		if err := svc.DeleteQBNode(id); err != nil {
			hzErr(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "server_id": id})
	}
}

// GetHetznerQBStatus GET /api/hetzner/qb/status（各节点 qB 状态与流量，仅查询展示）。
func GetHetznerQBStatus(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, err := hzService(state)
		if err != nil {
			hzNoToken(c)
			return
		}
		status, err := svc.QBStatus(hzCtx(c))
		if err != nil {
			hzErr(c, http.StatusInternalServerError, err)
			return
		}
		if status == nil {
			status = map[string]hetzner.QBStats{}
		}
		c.JSON(http.StatusOK, gin.H{"nodes": status})
	}
}

// GetHetznerAction GET /api/hetzner/actions/:id。
// 先查后台 job（12 位 job id）；查不到再按 Hetzner action id 问 API。
func GetHetznerAction(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc, ok := hzRequireToken(state)
		if !ok {
			hzNoToken(c)
			return
		}
		id := c.Param("id")
		if job := svc.Jobs().Get(id); job != nil {
			c.JSON(http.StatusOK, job)
			return
		}
		actionID, err := strconv.ParseInt(id, 10, 64)
		if err != nil || actionID <= 0 {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "job or action not found: " + id})
			return
		}
		act, err := svc.GetActionStatus(hzCtx(c), actionID)
		if err != nil {
			hzErr(c, http.StatusBadGateway, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "action": act})
	}
}
