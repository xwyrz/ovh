import { ShieldAlert, ShieldCheck, AlertTriangle, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import {
  useHetznerAction,
  useHetznerActionToast,
  type HetznerServerTraffic,
} from "@/hooks/use-hetzner";
import { formatBytes } from "@/hooks/use-hetzner";

/** SAFE_MODE 提示条:开启=仅告警不自动重建;关闭=会真重建,要显眼提醒 */
export function SafeModeBanner({ safeMode }: { safeMode: boolean }) {
  const { t } = useTranslation();
  return (
    <div
      role="status"
      className={cn(
        "flex items-start gap-2 rounded-xl border px-3 py-2 text-[12px]",
        safeMode
          ? "border-warning/40 bg-warning/5"
          : "border-destructive/40 bg-destructive/5",
      )}
    >
      {safeMode ? (
        <ShieldCheck className="w-4 h-4 text-warning flex-shrink-0 mt-0.5" />
      ) : (
        <ShieldAlert className="w-4 h-4 text-destructive flex-shrink-0 mt-0.5" />
      )}
      <p className="font-medium">{safeMode ? t("hetzner.safeModeOn") : t("hetzner.safeModeOff")}</p>
    </div>
  );
}

/** 未配 Token 空态:不打错误堆栈,给出去设置页的入口 */
export function NotConfiguredBanner({ onGoSettings }: { onGoSettings: () => void }) {
  const { t } = useTranslation();
  return (
    <div
      role="status"
      className="flex flex-col sm:flex-row sm:items-center gap-3 rounded-xl border border-warning/40 bg-warning/5 px-4 py-3"
    >
      <div className="flex items-start gap-2 flex-1 min-w-0">
        <AlertTriangle className="w-4 h-4 text-warning flex-shrink-0 mt-0.5" />
        <div>
          <p className="text-[13px] font-semibold">{t("hetzner.notConfiguredTitle")}</p>
          <p className="text-[12px] text-muted-foreground mt-0.5">{t("hetzner.notConfiguredDesc")}</p>
        </div>
      </div>
      <Button size="sm" onClick={onGoSettings}>
        {t("hetzner.goSettings")}
      </Button>
    </div>
  );
}

/**
 * 耗时操作轮询横幅:排队/执行中显示转圈,完成/失败时弹 toast 并刷新列表,
 * 然后横幅自行消失(调用方在 onDone 里把 actionId 清掉)。
 */
export function ActionStatusBanner({
  actionId,
  label,
  onDone,
}: {
  actionId: string | null;
  label: string;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const action = useHetznerAction(actionId);
  useHetznerActionToast(action, label, onDone);
  if (!actionId || !action) return null;
  const status = action.data?.status;
  if (status === "success" || status === "error") return null;
  return (
    <div role="status" className="flex items-center gap-2 rounded-xl border border-border bg-secondary/30 px-3 py-2 text-[12px]">
      <Loader2 className="w-4 h-4 animate-spin flex-shrink-0" />
      <span>
        {t("hetzner.action.polling", { label })}
        {status === "queued" ? ` · ${t("hetzner.action.queued")}` : ""}
      </span>
    </div>
  );
}

/** 流量百分比进度条(红 >90% / 黄 >70% / 默认) */
export function TrafficBar({ traffic }: { traffic?: HetznerServerTraffic | null }) {
  const { t } = useTranslation();
  if (!traffic) return <span className="text-muted-foreground">—</span>;
  const pct = Math.max(0, Math.min(100, Math.round(traffic.percent * 100)));
  const tone = pct >= 90 ? "bg-destructive" : pct >= 70 ? "bg-warning" : "bg-primary";
  return (
    <div className="min-w-[140px]">
      <div
        className="h-2 rounded-full bg-secondary overflow-hidden"
        role="progressbar"
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
        title={t("hetzner.trafficUsed", {
          used: formatBytes(traffic.used_bytes),
          limit: formatBytes(traffic.limit_bytes),
          pct,
        })}
      >
        <div className={cn("h-full rounded-full transition-all", tone)} style={{ width: `${pct}%` }} />
      </div>
      <p className="text-[11px] text-muted-foreground tabular-nums mt-1">
        {t("hetzner.trafficUsed", {
          used: formatBytes(traffic.used_bytes),
          limit: formatBytes(traffic.limit_bytes),
          pct,
        })}
      </p>
    </div>
  );
}
