import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { api } from "@/lib/api";
import { qk } from "@/lib/query";
import { toast } from "sonner";
import i18n from "@/i18n";
import { apiMessage } from "@/lib/api-error";

/**
 * Hetzner 数据层:全部走 @/lib/api 的 axios 实例(同源 /api + X-API-Key),
 * 与后端约定的契约字段一一对应。
 *
 * 后端实际返回形状(联调后核对,2026-11-24):
 * - 列表端点包在对象里:{servers:[...]}、{snapshots:[...]}、{policies:[...]}、
 *   {nodes:[...]};本文件做 unwrap,裸数组也兼容。
 * - 耗时操作(创建/重建/删除)返回 202 {status:"queued", job_id},job_id 即
 *   GET /api/hetzner/actions/:id 的轮询 id;同步完成的操作无 id,直接当成功。
 * - GET /servers/:id/traffic 返回 {server_id, days:<天数>, daily:[{date, bytes}],
 *   archived:[...]},daily 只有出站流量(入站暂无数据,图表只画出站)。
 * - GET /snapshots/:id/estimate 返回 {estimated_snapshot_size_gb,
 *   estimated_monthly_eur,...},本文件映射为 {size_gb, price, currency:"EUR"}。
 * - GET /qb/status 返回 {nodes:{<server_id>: {...}}} 的 map,本文件转成数组
 *   并把 key 填回 server_id。
 * - GET /snapshots 列表项与 GET /policies 列表项均为 snake_case JSON。
 * - 未配 Token 时后端返回 4xx:调用方用 isNotConfigured() 识别,页面走
 *   "请先在设置里填写 Hetzner Token"空态,不当成普通读取失败。
 */

export interface HetznerStatus {
  configured: boolean;
  safe_mode: boolean;
  traffic_limit_tb: number;
  rotate_threshold: number;
  check_interval_minutes: number;
}

export interface HetznerServerTraffic {
  used_bytes: number;
  limit_bytes: number;
  percent: number;
}

export interface HetznerServer {
  id: number;
  name: string;
  status: string;
  server_type: string;
  location: string;
  ipv4: string;
  ipv6: string;
  created: string;
  traffic: HetznerServerTraffic;
  qb_configured: boolean;
}

export interface HetznerTrafficDay {
  date: string;
  outbound_bytes: number;
  inbound_bytes: number;
}

export interface HetznerSnapshot {
  id: number;
  server_id: number;
  description: string;
  size_gb: number;
  created: string;
  /** 后端若返回则显示,否则不依赖 */
  name?: string;
  status?: string;
}

export interface HetznerSnapshotEstimate {
  size_gb: number;
  price: number;
  currency: string;
}

export interface HetznerPolicy {
  server_id: number;
  enabled: boolean;
  threshold: number;
  image_id: string;
}

export interface HetznerSettings {
  traffic_limit_tb: number;
  rotate_threshold: number;
  check_interval_minutes: number;
  safe_mode: boolean;
  /** 只写不读:PUT 时为空表示不改,GET 永远不回显 */
  hetzner_token?: string;
}

export interface HetznerQbNode {
  server_id: number;
  url: string;
  username: string;
}

export interface HetznerQbStatusEntry {
  server_id: number;
  connection_status?: string;
  up_speed?: number;
  dl_speed?: number;
  active_torrents?: number;
  all_torrents?: number;
  up_total?: number;
  dl_total?: number;
}

export interface HetznerAction {
  id: string | number;
  status: "queued" | "running" | "success" | "error";
  result?: string;
  error?: string;
}

/** 4xx = 未配 Token(或 Token 无效)——页面走"去设置"空态,不打错误堆栈 */
export function isNotConfigured(error: unknown): boolean {
  const status = (error as { response?: { status?: number } })?.response?.status;
  return typeof status === "number" && status >= 400 && status < 500;
}

/** 字节数 → 人类可读(IEC,跟 hzc 原项目的 formatIEC 同口径) */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
  const v = bytes / 1024 ** i;
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(v >= 10 ? 1 : 2)} ${units[i]}`;
}

/** B/s → 人类可读速率 */
export function formatBytesPerSec(bps: number): string {
  if (!Number.isFinite(bps) || bps < 0) return "—";
  return `${formatBytes(bps)}/s`;
}

/** 服务器状态 → i18n 文案(未知状态原样显示,不编造) */
export function serverStatusLabel(status: string): string {
  const key = `hetzner.status.${status}`;
  return i18n.exists(key) ? (i18n.t(key) as string) : status || (i18n.t("hetzner.status.unknown") as string);
}

/** 从耗时操作响应里抠动作 id:兼容 {job_id} / {id} / {action_id} / {action:{id}} */
export function extractActionId(data: unknown): string | null {
  if (!data || typeof data !== "object") return null;
  const d = data as Record<string, unknown>;
  const direct = d.job_id ?? d.id ?? d.action_id;
  if (typeof direct === "string" || typeof direct === "number") return String(direct);
  const nested = d.action as Record<string, unknown> | undefined;
  if (nested && (typeof nested.id === "string" || typeof nested.id === "number")) return String(nested.id);
  return null;
}

/** 列表响应可能是裸数组,也可能是包在对象里的 {<key>: [...]};两种都接受 */
function unwrapList<T>(data: unknown, key: string): T[] {
  if (Array.isArray(data)) return data as T[];
  if (data && typeof data === "object") {
    const v = (data as Record<string, unknown>)[key];
    if (Array.isArray(v)) return v as T[];
  }
  return [];
}

function invalidateHetzner(qc: ReturnType<typeof useQueryClient>) {
  qc.invalidateQueries({ queryKey: ["hetzner"] });
}

// ---------------------------------------------------------------- queries

export function useHetznerStatus() {
  return useQuery({
    queryKey: qk.hetzner.status(),
    queryFn: async () => (await api.get<HetznerStatus>("/hetzner/status")).data,
  });
}

export function useHetznerServers() {
  return useQuery({
    queryKey: qk.hetzner.servers(),
    queryFn: async () => {
      const d = (await api.get("/hetzner/servers")).data;
      return unwrapList<HetznerServer>(d, "servers");
    },
  });
}

export function useHetznerServer(id: number | null) {
  return useQuery({
    queryKey: qk.hetzner.server(id ?? 0),
    queryFn: async () => (await api.get<HetznerServer>(`/hetzner/servers/${id}`)).data,
    enabled: id !== null,
  });
}

export function useHetznerTraffic(id: number | null, days = 30) {
  return useQuery({
    queryKey: qk.hetzner.traffic(id ?? 0, days),
    queryFn: async (): Promise<{ days: HetznerTrafficDay[] }> => {
      const d = (await api.get(`/hetzner/servers/${id}/traffic`, { params: { days } })).data as {
        daily?: { date: string; bytes: number }[];
        days?: { date: string; outbound_bytes: number; inbound_bytes: number }[] | number;
        archived?: { date: string; bytes: number }[];
      };
      // 后端返回 {daily:[{date, bytes}]}(只有出站);days 字段是天数,不是数组。
      const raw = Array.isArray(d?.daily) ? d.daily : Array.isArray(d?.days) ? (d.days as { date: string; outbound_bytes: number; inbound_bytes: number }[]) : [];
      const mapped: HetznerTrafficDay[] = raw.map((p) => {
        if (typeof (p as { bytes?: unknown }).bytes === "number") {
          const q = p as { date: string; bytes: number };
          return { date: q.date, outbound_bytes: q.bytes, inbound_bytes: 0 };
        }
        const q = p as { date: string; outbound_bytes: number; inbound_bytes: number };
        return { date: q.date, outbound_bytes: q.outbound_bytes ?? 0, inbound_bytes: q.inbound_bytes ?? 0 };
      });
      return { days: mapped };
    },
    enabled: id !== null,
  });
}

export function useHetznerSnapshots() {
  return useQuery({
    queryKey: qk.hetzner.snapshots(),
    queryFn: async () => {
      const d = (await api.get("/hetzner/snapshots")).data;
      return unwrapList<HetznerSnapshot>(d, "snapshots");
    },
  });
}

export function useHetznerSnapshotEstimate(serverId: number | null) {
  return useQuery({
    queryKey: qk.hetzner.snapshotEstimate(serverId ?? 0),
    queryFn: async (): Promise<HetznerSnapshotEstimate> => {
      const d = (await api.get(`/hetzner/snapshots/${serverId}/estimate`)).data as Record<string, unknown>;
      // 后端字段:estimated_snapshot_size_gb / estimated_monthly_eur(欧元);
      // 已映射过的 {size_gb, price, currency} 也直接接受。
      return {
        size_gb: (d.size_gb as number) ?? (d.estimated_snapshot_size_gb as number) ?? 0,
        price: (d.price as number) ?? (d.estimated_monthly_eur as number) ?? 0,
        currency: (d.currency as string) ?? "EUR",
      };
    },
    enabled: serverId !== null,
  });
}

export function useHetznerPolicies() {
  return useQuery({
    queryKey: qk.hetzner.policies(),
    queryFn: async () => {
      const d = (await api.get("/hetzner/policies")).data;
      return unwrapList<HetznerPolicy>(d, "policies");
    },
  });
}

export function useHetznerSettings() {
  return useQuery({
    queryKey: qk.hetzner.settings(),
    queryFn: async () => (await api.get<HetznerSettings>("/hetzner/settings")).data,
  });
}

export function useHetznerQbNodes() {
  return useQuery({
    queryKey: qk.hetzner.qbNodes(),
    queryFn: async () => {
      const d = (await api.get("/hetzner/qb/nodes")).data;
      return unwrapList<HetznerQbNode>(d, "nodes");
    },
  });
}

export function useHetznerQbStatus() {
  return useQuery({
    queryKey: qk.hetzner.qbStatus(),
    queryFn: async (): Promise<HetznerQbStatusEntry[]> => {
      const d = (await api.get("/hetzner/qb/status")).data as unknown;
      // 后端返回 {nodes:{<server_id>: {...}}} 的 map;裸数组也接受。
      if (Array.isArray(d)) return d as HetznerQbStatusEntry[];
      const map = (d as { nodes?: Record<string, HetznerQbStatusEntry> } | null)?.nodes;
      if (map && typeof map === "object") {
        return Object.entries(map).map(([k, v]) => ({ ...v, server_id: Number(k) }));
      }
      return [];
    },
    refetchInterval: 30_000,
  });
}

/**
 * 轮询单个耗时操作:queued/running 时每 2 秒查一次,到 success/error 停。
 * 返回 null 表示未开始轮询。
 */
export function useHetznerAction(actionId: string | null) {
  const q = useQuery({
    queryKey: qk.hetzner.action(actionId ?? ""),
    queryFn: async () => (await api.get<HetznerAction>(`/hetzner/actions/${actionId}`)).data,
    enabled: actionId !== null,
    refetchInterval: (query) => {
      const s = query.state.data?.status;
      return s === "success" || s === "error" ? false : 2000;
    },
  });
  return actionId === null ? null : q;
}

/**
 * 动作完成后的 toast:成功 / 失败各报一次(terminal 状态才报,
 * 避免轮询中途重复弹)。
 */
export function useHetznerActionToast(
  action: ReturnType<typeof useHetznerAction>,
  label: string,
  onDone?: () => void,
) {
  const status = action?.data?.status;
  useEffect(() => {
    if (!action || !status) return;
    if (status === "success") {
      const result = action.data?.result as unknown;
      const text = typeof result === "string" ? result : result == null ? "" : JSON.stringify(result);
      toast.success(
        text
          ? i18n.t("hetzner.action.result", { result: text })
          : i18n.t("hetzner.action.success"),
      );
      onDone?.();
    } else if (status === "error") {
      toast.error(action.data?.error || i18n.t("hetzner.action.failed"));
      onDone?.();
    }
    // 只在 terminal 状态变化时触发一次;label/onDone 不应反复触发
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status]);
}

// -------------------------------------------------------------- mutations

/** 提交耗时操作:返回动作 id 供轮询;后端直接同步完成(无 id)时当成功处理 */
function useHetznerOpMutation(successKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (fn: () => Promise<unknown>) => fn(),
    onSuccess: (data) => {
      // mutationFn 返回 axios 响应整体,动作体在 .data 里
      const payload = (data as { data?: unknown } | null)?.data ?? data;
      const id = extractActionId(payload);
      if (!id) {
        toast.success(i18n.t("hetzner.action.success"));
        invalidateHetzner(qc);
      }
      // 有 id 时由调用方把 id 交给 useHetznerAction 轮询,完成后再刷新
      void successKey;
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.opFailed")),
  });
}

export function useHetznerCheck() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => (await api.post("/hetzner/check")).data,
    onSuccess: () => {
      toast.success(i18n.t("hetzner.checkDone"));
      invalidateHetzner(qc);
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.checkFailed")),
  });
}

export function useCreateHetznerServer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (payload: {
      name: string;
      server_type: string;
      location: string;
      image: string;
      ssh_keys?: string[];
      primary_ipv4_id?: string;
      primary_ipv6_id?: string;
    }) => (await api.post("/hetzner/servers", payload)).data,
    onSuccess: (data) => {
      if (!extractActionId(data)) {
        toast.success(i18n.t("hetzner.toast.created"));
        invalidateHetzner(qc);
      }
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.createFailed")),
  });
}

export function useHetznerServerOp() {
  const qc = useQueryClient();
  const base = useHetznerOpMutation("hetzner-op");
  return {
    ...base,
    rebuild: (id: number, image_id?: string) =>
      base.mutateAsync(() => api.post(`/hetzner/servers/${id}/rebuild`, image_id ? { image_id } : {})),
    rebuildFull: (id: number, image_id: string) =>
      base.mutateAsync(() => api.post(`/hetzner/servers/${id}/rebuild-full`, { image_id })),
    reboot: (id: number) => base.mutateAsync(() => api.post(`/hetzner/servers/${id}/reboot`)),
    hardReboot: (id: number) => base.mutateAsync(() => api.post(`/hetzner/servers/${id}/hard-reboot`)),
    resetPassword: (id: number) => base.mutateAsync(() => api.post(`/hetzner/servers/${id}/reset-password`)),
    invalidate: () => invalidateHetzner(qc),
  };
}

export function useRenameHetznerServer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { id: number; name: string }) =>
      (await api.patch(`/hetzner/servers/${v.id}`, { name: v.name })).data,
    onSuccess: () => {
      toast.success(i18n.t("hetzner.toast.renamed"));
      invalidateHetzner(qc);
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.renameFailed")),
  });
}

export function useDeleteHetznerServer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { id: number; create_snapshot: boolean; keep_ipv4: boolean; keep_ipv6: boolean }) =>
      (await api.delete(`/hetzner/servers/${v.id}`, {
        data: { create_snapshot: v.create_snapshot, keep_ipv4: v.keep_ipv4, keep_ipv6: v.keep_ipv6 },
      })).data,
    onSuccess: (data) => {
      if (!extractActionId(data)) {
        toast.success(i18n.t("hetzner.toast.deleted"));
        invalidateHetzner(qc);
      }
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.deleteFailed")),
  });
}

export function useCreateHetznerSnapshot() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { server_id: number; description: string }) =>
      (await api.post("/hetzner/snapshots", v)).data,
    onSuccess: (data) => {
      if (!extractActionId(data)) {
        toast.success(i18n.t("hetzner.toast.snapshotCreated"));
        invalidateHetzner(qc);
      }
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.snapshotCreateFailed")),
  });
}

export function useDeleteHetznerSnapshot() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: number) => (await api.delete(`/hetzner/snapshots/${id}`)).data,
    onSuccess: () => {
      toast.success(i18n.t("hetzner.toast.snapshotDeleted"));
      invalidateHetzner(qc);
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.snapshotDeleteFailed")),
  });
}

export function useRenameHetznerSnapshot() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { id: number; description: string }) =>
      (await api.patch(`/hetzner/snapshots/${v.id}`, { description: v.description })).data,
    onSuccess: () => {
      toast.success(i18n.t("hetzner.toast.snapshotRenamed"));
      invalidateHetzner(qc);
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.snapshotRenameFailed")),
  });
}

export function useSaveHetznerPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { server_id: number; enabled: boolean; threshold: number; image_id: string }) =>
      (await api.post("/hetzner/policies", v)).data,
    onSuccess: () => {
      toast.success(i18n.t("hetzner.toast.policySaved"));
      invalidateHetzner(qc);
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.policySaveFailed")),
  });
}

export function useDeleteHetznerPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (serverId: number) => (await api.delete(`/hetzner/policies/${serverId}`)).data,
    onSuccess: () => {
      toast.success(i18n.t("hetzner.toast.policyDeleted"));
      invalidateHetzner(qc);
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.policyDeleteFailed")),
  });
}

export function useSaveHetznerSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: HetznerSettings) => {
      // token 为空表示不改:不发该字段,避免后端误当成"清空"
      const { hetzner_token, ...rest } = v;
      const payload = hetzner_token ? { ...rest, hetzner_token } : rest;
      return (await api.put("/hetzner/settings", payload)).data;
    },
    onSuccess: () => {
      toast.success(i18n.t("hetzner.toast.settingsSaved"));
      invalidateHetzner(qc);
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.settingsSaveFailed")),
  });
}

export function useSaveHetznerQbNode() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { server_id: number; url: string; username: string; password: string }) =>
      (await api.post("/hetzner/qb/nodes", v)).data,
    onSuccess: () => {
      toast.success(i18n.t("hetzner.toast.qbSaved"));
      invalidateHetzner(qc);
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.qbSaveFailed")),
  });
}

export function useDeleteHetznerQbNode() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (serverId: number) => (await api.delete(`/hetzner/qb/nodes/${serverId}`)).data,
    onSuccess: () => {
      toast.success(i18n.t("hetzner.toast.qbDeleted"));
      invalidateHetzner(qc);
    },
    onError: (e: unknown) => toast.error(apiMessage(e) || i18n.t("hetzner.toast.qbDeleteFailed")),
  });
}
