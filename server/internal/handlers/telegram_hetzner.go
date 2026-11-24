package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ovh-buy/server/internal/app"
	"github.com/ovh-buy/server/internal/hetzner"
)

// Hetzner 的 Telegram 命令（/hz_ 前缀），并进现有机器人，不另起 bot。
// 至少实现：状态查询、流量查询、重建、快照列表。

// hzTGService 取服务；未接线/未配 token 返回错误文本（直接回给用户）。
func hzTGService(state *app.State) (*hetzner.Service, string) {
	svc, err := hzService(state)
	if err != nil {
		return nil, "Hetzner 未配置：请先在设置里填写 Hetzner Token。"
	}
	if !svc.Configured() {
		return nil, "Hetzner 未配置：请先在设置里填写 Hetzner Token。"
	}
	return svc, ""
}

func hzStatusText(state *app.State) string {
	svc, msg := hzTGService(state)
	if svc == nil {
		return msg
	}
	rows, err := svc.CollectServers(context.Background())
	if err != nil {
		return "Hetzner 状态查询失败：" + err.Error()
	}
	st := svc.Settings()
	over := 0
	for _, r := range rows {
		if r.Traffic.Percent >= st.RotateThreshold {
			over++
		}
	}
	return fmt.Sprintf("🖥️ Hetzner 状态\n服务器：%d 台\n超阈值：%d 台\nSAFE_MODE：%v\n流量上限：%.0f TB\n阈值：%.2f\n检查间隔：%d 分钟",
		len(rows), over, st.SafeMode, st.TrafficLimitTB, st.RotateThreshold, st.CheckIntervalMin)
}

func hzListText(state *app.State) string {
	svc, msg := hzTGService(state)
	if svc == nil {
		return msg
	}
	rows, err := svc.CollectServers(context.Background())
	if err != nil {
		return "Hetzner 服务器列表查询失败：" + err.Error()
	}
	if len(rows) == 0 {
		return "暂无 Hetzner 服务器"
	}
	var b strings.Builder
	b.WriteString("🖥️ Hetzner 服务器列表")
	for _, r := range rows {
		pct := r.Traffic.Percent * 100
		state := "🟢运行"
		if r.Status != "running" {
			state = "🟠" + r.Status
		}
		fmt.Fprintf(&b, "\n\n%s  %d\n%s · 🌐 %s\n📤 %.4f/%.0f TB (%.2f%%)",
			r.Name, r.ID, state, r.IPv4,
			float64(r.Traffic.UsedBytes)/float64(hetzner.BytesInTB),
			float64(r.Traffic.LimitBytes)/float64(hetzner.BytesInTB), pct)
	}
	return b.String()
}

func hzTrafficText(state *app.State, args []string) string {
	svc, msg := hzTGService(state)
	if svc == nil {
		return msg
	}
	if len(args) < 1 {
		return "用法：/hz_traffic <服务器ID>"
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return "服务器 ID 不合法：" + args[0]
	}
	rows, err := svc.CollectServers(context.Background())
	if err != nil {
		return "Hetzner 流量查询失败：" + err.Error()
	}
	for _, r := range rows {
		if r.ID != id {
			continue
		}
		return fmt.Sprintf("%s (ID: %d)\n本月已用：%.4f TB / %.0f TB\n占比：%.2f%%\n今日：%.2f GB",
			r.Name, r.ID,
			float64(r.Traffic.UsedBytes)/float64(hetzner.BytesInTB),
			float64(r.Traffic.LimitBytes)/float64(hetzner.BytesInTB),
			r.Traffic.Percent*100,
			float64(r.TodayBytes)/1024/1024/1024)
	}
	return fmt.Sprintf("未找到 Hetzner 服务器：%d", id)
}

func hzSnapshotsText(state *app.State) string {
	svc, msg := hzTGService(state)
	if svc == nil {
		return msg
	}
	snaps, err := svc.ListSnapshots(context.Background())
	if err != nil {
		return "Hetzner 快照列表查询失败：" + err.Error()
	}
	if len(snaps) == 0 {
		return "暂无 Hetzner 快照"
	}
	var b strings.Builder
	b.WriteString("🧊 Hetzner 快照列表")
	for i, sn := range snaps {
		if i >= 30 {
			break
		}
		fmt.Fprintf(&b, "\n#%d %s %.2fGB", sn.ID, sn.Description, sn.SizeGB)
	}
	return b.String()
}

// hzRebuildText /hz_rebuild <服务器ID> <快照/镜像ID>：走后台 job，不在 TG 里等。
func hzRebuildText(state *app.State, args []string) string {
	svc, msg := hzTGService(state)
	if svc == nil {
		return msg
	}
	if len(args) < 2 {
		return "用法：/hz_rebuild <服务器ID> <快照/镜像ID>\n例如：/hz_rebuild 123 456789"
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return "服务器 ID 不合法：" + args[0]
	}
	image := hetzner.ParseImageRef(args[1])
	if image.IsZero() {
		return "镜像/快照 ID 不合法：" + args[1]
	}
	jobID := svc.RunJob("rebuild", func() (interface{}, error) {
		return svc.Rebuild(context.Background(), id, image)
	})
	return fmt.Sprintf("♻️ Hetzner 重建已提交（保留原 IP）\n服务器：%d\n镜像：%s\n任务：%s\n完成后会发通知。", id, image.String(), jobID)
}
