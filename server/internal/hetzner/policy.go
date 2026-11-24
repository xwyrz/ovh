package hetzner

// PolicyDecision 自动重建策略评估的纯判定结果（不含任何副作用，可单测）。
type PolicyDecision struct {
	// Action: none | warn | alert_threshold | alert_no_image | rebuild |
	//         auto_poweroff | alert_poweroff
	Action string
	// Reason 给人看的原因（中文）。
	Reason string
	// UsedTB / LimitTB 判定用的实际数值（实时校准后）。
	UsedTB  float64
	LimitTB float64
}

// PolicyInput 判定输入。
type PolicyInput struct {
	Enabled   bool
	Threshold float64 // 占比阈值 (0,1]
	ImageID   string  // 重建镜像/快照（空=未配）
	UsedTB    float64 // 本月已用（实时校准后优先）
	LimitTB   float64 // 月限额
	SafeMode  bool
	Guard     GuardEntry // 该服务器的守卫状态
	NowUnix   int64
}

// DecidePolicyAction 对照 hzc service.py rotate_if_needed 的判定重写：
//
//   - 未配策略：>= 限额 → 自动关机（只执行一次）；>= 限额-1TB → 每 10 分钟告警一次；
//     低于限额-1TB → 清理守卫状态。
//     与 hzc 的一处不同：SAFE_MODE 开时自动关机也不执行，只告警
//     （hzc 里这条分支不看 safe_mode；但任务要求 SAFE_MODE=true 时“只告警
//     不自动执行危险动作”，关机是危险动作，所以这里以任务为准）。
//   - 已配策略且超阈值：SAFE_MODE → 只告警；未配镜像 → 告警；否则重建。
func DecidePolicyAction(in PolicyInput) PolicyDecision {
	limit := in.LimitTB
	if limit <= 0 {
		limit = DefaultTrafficLimitTB
	}
	threshold := in.Threshold
	if threshold <= 0 || threshold > 1 {
		threshold = DefaultRotateThreshold
	}
	used := in.UsedTB
	over := limit > 0 && used/limit >= threshold

	if !in.Enabled {
		if used >= limit {
			if in.Guard.AutoStopped {
				return PolicyDecision{Action: "none", Reason: "已自动关机过，不再重复执行", UsedTB: used, LimitTB: limit}
			}
			if in.SafeMode {
				return PolicyDecision{Action: "alert_poweroff", Reason: "已达限额，SAFE_MODE 下只告警不自动关机", UsedTB: used, LimitTB: limit}
			}
			return PolicyDecision{Action: "auto_poweroff", Reason: "未开启自动重建且已达限额，自动关机", UsedTB: used, LimitTB: limit}
		}
		if used >= limit-1 {
			if in.NowUnix-in.Guard.LastWarnTS >= 600 {
				return PolicyDecision{Action: "warn", Reason: "接近限额（未开启自动重建，达到限额将自动关机）", UsedTB: used, LimitTB: limit}
			}
			return PolicyDecision{Action: "none", Reason: "接近限额但在 10 分钟告警节流内", UsedTB: used, LimitTB: limit}
		}
		// 低于限额-1TB：调用方清理守卫状态。
		return PolicyDecision{Action: "none", Reason: "流量正常", UsedTB: used, LimitTB: limit}
	}

	if !over {
		return PolicyDecision{Action: "none", Reason: "未达阈值", UsedTB: used, LimitTB: limit}
	}
	if in.SafeMode {
		return PolicyDecision{Action: "alert_threshold", Reason: "达到自动阈值，SAFE_MODE 下仅通知不执行", UsedTB: used, LimitTB: limit}
	}
	if in.ImageID == "" {
		return PolicyDecision{Action: "alert_no_image", Reason: "达到自动阈值但未配置重建镜像/快照", UsedTB: used, LimitTB: limit}
	}
	return PolicyDecision{Action: "rebuild", Reason: "达到自动阈值，执行重建", UsedTB: used, LimitTB: limit}
}
