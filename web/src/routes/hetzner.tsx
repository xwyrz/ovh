import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useEffect, useMemo, useState } from "react";
import {
  Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis,
} from "recharts";
import { Gauge, HardDrive, RefreshCw, Server, ShieldCheck, Zap, ArrowRight } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/common/Skeleton";
import { EmptyState } from "@/components/common/EmptyState";
import { LoadFailed, LoadFailedBanner } from "@/components/common/LoadFailed";
import { StatusDot } from "@/components/common/StatusDot";
import { Chip } from "@/components/common/Chip";
import {
  useHetznerStatus, useHetznerServers, useHetznerTraffic, useHetznerPolicies,
  useHetznerSettings, useSaveHetznerSettings, useHetznerCheck,
  isNotConfigured, formatBytes, serverStatusLabel,
} from "@/hooks/use-hetzner";
import { SafeModeBanner, NotConfiguredBanner, TrafficBar } from "@/components/hetzner/HetznerShared";
import { fmtDate } from "@/i18n/format";

/** Hetzner 仪表盘:总览卡片 + 流量趋势图 + 服务器列表摘要 + 设置 */
export const Route = createFileRoute("/hetzner")({
  component: HetznerDashboardPage,
});

function HetznerDashboardPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState("overview");
  const statusQ = useHetznerStatus();
  const serversQ = useHetznerServers();
  const policiesQ = useHetznerPolicies();
  const check = useHetznerCheck();

  // 未配 Token:status 明确 configured=false,或任一核心接口报 4xx。
  // 跟"读取失败"分开:前者去设置页填 Token,后者重试。
  const notConfigured =
    statusQ.data?.configured === false ||
    (statusQ.isError && isNotConfigured(statusQ.error)) ||
    (serversQ.isError && isNotConfigured(serversQ.error));
  const statusFailed = statusQ.isError && !isNotConfigured(statusQ.error);
  const listFailed = serversQ.isError && !isNotConfigured(serversQ.error);

  const servers = serversQ.data || [];
  const online = servers.filter((s) => s.status === "running").length;
  const totalUsed = servers.reduce((a, s) => a + (s.traffic?.used_bytes || 0), 0);
  const totalLimit = servers.reduce((a, s) => a + (s.traffic?.limit_bytes || 0), 0);
  const enabledPolicies = (policiesQ.data || []).filter((p) => p.enabled).length;

  const refreshAll = () => {
    statusQ.refetch();
    serversQ.refetch();
    policiesQ.refetch();
  };

  return (
    <div className="space-y-3 sm:space-y-6">
      <PageHeader
        icon={Gauge}
        title={t("hetzner.title")}
        description={t("hetzner.description")}
        action={
          <div className="flex flex-wrap items-center justify-end gap-2">
            <Button
              variant="outline"
              onClick={() => check.mutate()}
              disabled={check.isPending || notConfigured}
              title={t("hetzner.checkNow")}
            >
              <Zap className="w-4 h-4" />
              {check.isPending ? t("hetzner.checking") : t("hetzner.checkNow")}
            </Button>
            <Button variant="outline" onClick={refreshAll} disabled={statusQ.isFetching || serversQ.isFetching}>
              <RefreshCw className={`w-4 h-4 ${statusQ.isFetching || serversQ.isFetching ? "animate-spin" : ""}`} />
              {t("hetzner.refresh")}
            </Button>
          </div>
        }
      />

      {statusQ.data && <SafeModeBanner safeMode={statusQ.data.safe_mode} />}
      {notConfigured && <NotConfiguredBanner onGoSettings={() => setTab("settings")} />}
      {statusFailed && (
        <LoadFailedBanner title={t("hetzner.statusFailedTitle")} error={statusQ.error} onRetry={() => statusQ.refetch()} />
      )}

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="overview">{t("hetzner.dashboard")}</TabsTrigger>
          <TabsTrigger value="settings">{t("hetzner.settings")}</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="space-y-3 sm:space-y-6 mt-4">
          {/* 总览卡片 */}
          {statusQ.isPending && serversQ.isPending ? (
            <div className="grid grid-cols-2 xl:grid-cols-4 gap-4">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-[104px] rounded-2xl" />
              ))}
            </div>
          ) : (
            <div className="grid grid-cols-2 xl:grid-cols-4 gap-4">
              <StatCard icon={<Server className="w-4 h-4" />} label={t("hetzner.serversCount")} value={String(servers.length)} />
              <StatCard icon={<StatusDot tone="success" pulse={online > 0} size="sm" />} label={t("hetzner.onlineCount")} value={String(online)} />
              <StatCard
                icon={<HardDrive className="w-4 h-4" />}
                label={t("hetzner.monthTraffic")}
                value={totalLimit > 0 ? formatBytes(totalUsed) : formatBytes(totalUsed)}
                sub={
                  totalLimit > 0
                    ? t("hetzner.totalUsed", { used: formatBytes(totalUsed), limit: formatBytes(totalLimit) })
                    : t("hetzner.noLimit")
                }
              />
              <StatCard
                icon={<ShieldCheck className="w-4 h-4" />}
                label={t("hetzner.policiesCount")}
                value={String((policiesQ.data || []).length)}
                sub={t("hetzner.policiesEnabled", { count: enabledPolicies })}
              />
            </div>
          )}

          {listFailed && (
            <LoadFailedBanner title={t("hetzner.listFailedTitle")} error={serversQ.error} onRetry={() => serversQ.refetch()} />
          )}

          {/* 流量趋势图:按单台服务器的日流量(契约没有聚合接口,选一台看) */}
          <TrafficChartCard servers={servers} loading={serversQ.isPending} />

          {/* 服务器列表摘要 */}
          <Card>
            <CardContent className="p-5 space-y-3">
              <div className="flex items-center justify-between gap-2 flex-wrap">
                <h3 className="text-sm font-semibold">{t("hetzner.summaryTitle")}</h3>
                <Link to="/hetzner-servers" className="inline-flex items-center gap-1 text-[12px] text-primary hover:underline">
                  {t("hetzner.viewAll")}
                  <ArrowRight className="w-3.5 h-3.5" />
                </Link>
              </div>
              {serversQ.isPending ? (
                <div className="space-y-2">
                  {Array.from({ length: 3 }).map((_, i) => (
                    <Skeleton key={i} className="h-[52px] rounded-xl" />
                  ))}
                </div>
              ) : servers.length === 0 ? (
                <EmptyState
                  illustration="servers"
                  title={t("hetzner.emptyServers")}
                  description={t("hetzner.emptyServersDesc")}
                  action={
                    <Link to="/hetzner-servers">
                      <Button size="sm">{t("hetzner.createServer")}</Button>
                    </Link>
                  }
                />
              ) : (
                <div className="divide-y divide-border">
                  {servers.slice(0, 5).map((s) => (
                    <div key={s.id} className="flex items-center gap-3 py-3 first:pt-0 last:pb-0 flex-wrap">
                      <StatusDot tone={s.status === "running" ? "success" : s.status === "off" ? "danger" : "warning"} size="sm" pulse={s.status === "running"} />
                      <div className="min-w-0 flex-1">
                        <p className="text-[13px] font-medium truncate">{s.name}</p>
                        <p className="text-[11px] text-muted-foreground truncate">
                          {s.server_type} · {s.location} · {s.ipv4 || "—"}
                        </p>
                      </div>
                      <Chip tone={s.status === "running" ? "success" : "default"}>{serverStatusLabel(s.status)}</Chip>
                      <div className="w-full sm:w-[220px]">
                        <TrafficBar traffic={s.traffic} />
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="settings" className="mt-4">
          <HetznerSettingsCard />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function StatCard({ icon, label, value, sub }: { icon: React.ReactNode; label: string; value: string; sub?: string }) {
  return (
    <Card>
      <CardContent className="p-4 sm:p-5">
        <div className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
          <span className="flex-shrink-0">{icon}</span>
          <span className="truncate">{label}</span>
        </div>
        <p className="text-xl sm:text-2xl font-bold tabular-nums mt-1.5 truncate">{value}</p>
        {sub && <p className="text-[11px] text-muted-foreground tabular-nums mt-0.5 truncate" title={sub}>{sub}</p>}
      </CardContent>
    </Card>
  );
}

/** 流量趋势图(单台服务器,最近 30 天出站面积图;后端目前只提供出站流量) */
function TrafficChartCard({ servers, loading }: { servers: { id: number; name: string }[]; loading: boolean }) {
  const { t } = useTranslation();
  const [serverId, setServerId] = useState<number | null>(null);
  const selectedId = serverId ?? servers[0]?.id ?? null;
  const trafficQ = useHetznerTraffic(selectedId, 30);
  const days = useMemo(
    () => (trafficQ.data?.days || []).map((d) => ({ ...d, label: fmtDate(d.date) })),
    [trafficQ.data],
  );

  return (
    <Card>
      <CardContent className="p-5 space-y-4">
        <div className="flex items-center justify-between gap-2 flex-wrap">
          <div>
            <h3 className="text-sm font-semibold">{t("hetzner.trafficChartTitle")}</h3>
            <p className="text-[11px] text-muted-foreground mt-0.5">{t("hetzner.trafficChartDesc", { days: 30 })}</p>
          </div>
          {servers.length > 0 && (
            <Select
              value={selectedId !== null ? String(selectedId) : ""}
              onValueChange={(v) => setServerId(Number(v))}
            >
              <SelectTrigger className="rounded-full h-9 w-52">
                <SelectValue placeholder={t("hetzner.chartServer")} />
              </SelectTrigger>
              <SelectContent>
                {servers.map((s) => (
                  <SelectItem key={s.id} value={String(s.id)}>{s.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </div>
        {loading || trafficQ.isPending ? (
          <Skeleton className="h-[240px] rounded-xl" />
        ) : trafficQ.isError ? (
          <LoadFailed
            compact
            title={t("hetzner.listFailedTitle")}
            error={trafficQ.error}
            onRetry={() => trafficQ.refetch()}
          />
        ) : days.length === 0 ? (
          <EmptyState illustration="search" title={t("hetzner.chartEmpty")} description={t("hetzner.chartSelectServer")} />
        ) : (
          <div className="h-[240px] w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={days} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
                <CartesianGrid strokeDasharray="3 3" opacity={0.3} />
                <XAxis dataKey="label" tick={{ fontSize: 11 }} tickMargin={8} minTickGap={32} />
                <YAxis tick={{ fontSize: 11 }} width={64} tickFormatter={(v: number) => formatBytes(v)} />
                <Tooltip
                  formatter={(v: number | string) => [formatBytes(Number(v)), t("hetzner.chartOutbound")]}
                  labelFormatter={(label: string) => label}
                />
                <Area type="monotone" dataKey="outbound_bytes" stroke="var(--primary)" fill="var(--primary)" fillOpacity={0.15} strokeWidth={2} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/** Hetzner 设置:Token(永不回显,空=不改)+ 限额/阈值/间隔/ SAFE_MODE */
function HetznerSettingsCard() {
  const { t } = useTranslation();
  const settingsQ = useHetznerSettings();
  const save = useSaveHetznerSettings();
  const [token, setToken] = useState("");
  const [trafficLimit, setTrafficLimit] = useState("");
  const [rotateThreshold, setRotateThreshold] = useState("");
  const [checkInterval, setCheckInterval] = useState("");
  const [safeMode, setSafeMode] = useState(true);
  const [hydrated, setHydrated] = useState(false);

  // 后端 GET 永远不返回 token:这里只回填数值项,token 输入框始终留空
  useEffect(() => {
    const d = settingsQ.data;
    if (!d || hydrated) return;
    setTrafficLimit(d.traffic_limit_tb !== undefined ? String(d.traffic_limit_tb) : "");
    setRotateThreshold(d.rotate_threshold !== undefined ? String(d.rotate_threshold) : "");
    setCheckInterval(d.check_interval_minutes !== undefined ? String(d.check_interval_minutes) : "");
    setSafeMode(!!d.safe_mode);
    setHydrated(true);
  }, [settingsQ.data, hydrated]);

  if (settingsQ.isPending) {
    return (
      <Card>
        <CardContent className="p-5">
          <Skeleton className="h-[280px] rounded-xl" />
        </CardContent>
      </Card>
    );
  }
  if (settingsQ.isError) {
    return (
      <Card>
        <LoadFailed title={t("hetzner.statusFailedTitle")} error={settingsQ.error} onRetry={() => settingsQ.refetch()} />
      </Card>
    );
  }

  return (
    <Card>
      <CardContent className="p-5 space-y-4 max-w-2xl">
        <div>
          <label htmlFor="hz-token" className="block text-[13px] font-medium mb-1.5">{t("hetzner.form.token")}</label>
          <Input
            id="hz-token"
            type="password"
            autoComplete="off"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder={t("hetzner.form.tokenPlaceholder")}
          />
          <p className="text-[11px] text-muted-foreground mt-1">{t("hetzner.form.tokenHint")}</p>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
          <div>
            <label htmlFor="hz-limit" className="block text-[13px] font-medium mb-1.5">{t("hetzner.form.trafficLimit")}</label>
            <Input id="hz-limit" type="number" min={0} step="0.1" value={trafficLimit} onChange={(e) => setTrafficLimit(e.target.value)} />
          </div>
          <div>
            <label htmlFor="hz-threshold" className="block text-[13px] font-medium mb-1.5">{t("hetzner.form.rotateThreshold")}</label>
            <Input id="hz-threshold" type="number" min={0} max={1} step="0.05" value={rotateThreshold} onChange={(e) => setRotateThreshold(e.target.value)} />
          </div>
          <div>
            <label htmlFor="hz-interval" className="block text-[13px] font-medium mb-1.5">{t("hetzner.form.checkInterval")}</label>
            <Input id="hz-interval" type="number" min={1} step={1} value={checkInterval} onChange={(e) => setCheckInterval(e.target.value)} />
          </div>
        </div>
        <div className="flex items-start justify-between gap-3 rounded-xl border border-border px-3.5 py-3">
          <div>
            <p className="text-[13px] font-medium">{t("hetzner.form.safeMode")}</p>
            <p className="text-[11px] text-muted-foreground mt-0.5">{t("hetzner.form.safeModeHint")}</p>
          </div>
          <Switch checked={safeMode} onCheckedChange={setSafeMode} aria-label={t("hetzner.form.safeMode")} />
        </div>
        <div className="flex justify-end">
          <Button
            disabled={save.isPending}
            onClick={() => {
              save.mutate(
                {
                  traffic_limit_tb: Number(trafficLimit) || 0,
                  rotate_threshold: Math.min(1, Math.max(0, Number(rotateThreshold) || 0)),
                  check_interval_minutes: Math.max(1, Math.round(Number(checkInterval) || 60)),
                  safe_mode: safeMode,
                  hetzner_token: token.trim() || undefined,
                },
                { onSuccess: () => setToken("") },
              );
            }}
          >
            {t("hetzner.form.save")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
