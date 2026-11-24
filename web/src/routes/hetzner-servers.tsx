import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import {
  Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis,
} from "recharts";
import {
  HardDrive, Plus, RefreshCw, RotateCcw, Power, AlertTriangle, Pencil,
  Trash2, Camera, ShieldCheck, Database, ChevronDown, Copy,
} from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Skeleton } from "@/components/common/Skeleton";
import { EmptyState } from "@/components/common/EmptyState";
import { LoadFailed, LoadFailedBanner } from "@/components/common/LoadFailed";
import { StatusDot } from "@/components/common/StatusDot";
import { Chip } from "@/components/common/Chip";
import { useConfirm } from "@/components/common/ConfirmDialog";
import {
  useHetznerStatus, useHetznerServers, useHetznerTraffic, useHetznerSnapshots,
  useHetznerSnapshotEstimate, useHetznerPolicies, useHetznerQbNodes, useHetznerQbStatus,
  useHetznerServerOp, useCreateHetznerServer, useRenameHetznerServer, useDeleteHetznerServer,
  useCreateHetznerSnapshot, useDeleteHetznerSnapshot, useRenameHetznerSnapshot,
  useSaveHetznerPolicy, useDeleteHetznerPolicy, useSaveHetznerQbNode, useDeleteHetznerQbNode,
  isNotConfigured, formatBytes, formatBytesPerSec, serverStatusLabel, extractActionId,
  type HetznerServer, type HetznerSnapshot, type HetznerPolicy,
} from "@/hooks/use-hetzner";
import { SafeModeBanner, NotConfiguredBanner, ActionStatusBanner, TrafficBar } from "@/components/hetzner/HetznerShared";
import { fmtDate, fmtDateTime } from "@/i18n/format";

/**
 * Hetzner 服务器管理:服务器列表与操作 / 快照 / 自动重建策略 / qB 节点。
 * qB 节点配置是可选能力,默认收起(details 折叠)。
 * 危险操作(重建/删除)一律经 useConfirm 弹窗写清后果;SAFE_MODE 开启时
 * 自动重建的启用开关禁用并注明"仅告警,不执行"。
 */
export const Route = createFileRoute("/hetzner-servers")({
  component: HetznerServersPage,
});

function HetznerServersPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [tab, setTab] = useState("servers");
  const [confirm, confirmDialog] = useConfirm();
  const statusQ = useHetznerStatus();
  const serversQ = useHetznerServers();
  const snapshotsQ = useHetznerSnapshots();
  const policiesQ = useHetznerPolicies();
  const qbNodesQ = useHetznerQbNodes();

  // 耗时操作轮询:任一操作提交后拿到 actionId 就在顶部显示进度横幅
  const [actionId, setActionId] = useState<string | null>(null);
  const [actionLabel, setActionLabel] = useState("");
  const ops = useHetznerServerOp();
  const trackAction = (res: unknown, label: string) => {
    const id = extractActionId((res as { data?: unknown } | null)?.data ?? res);
    if (id) {
      setActionId(id);
      setActionLabel(label);
    } else {
      ops.invalidate();
    }
  };

  const notConfigured =
    statusQ.data?.configured === false ||
    (statusQ.isError && isNotConfigured(statusQ.error)) ||
    (serversQ.isError && isNotConfigured(serversQ.error));
  const safeMode = !!statusQ.data?.safe_mode;

  const refreshAll = () => {
    statusQ.refetch();
    serversQ.refetch();
    snapshotsQ.refetch();
    policiesQ.refetch();
    qbNodesQ.refetch();
  };

  return (
    <div className="space-y-3 sm:space-y-6">
      <PageHeader
        icon={HardDrive}
        title={t("hetzner.serversTitle")}
        description={t("hetzner.serversDescription")}
        action={
          <div className="flex flex-wrap items-center justify-end gap-2">
            <Button variant="outline" onClick={refreshAll} disabled={serversQ.isFetching}>
              <RefreshCw className={`w-4 h-4 ${serversQ.isFetching ? "animate-spin" : ""}`} />
              {t("hetzner.refresh")}
            </Button>
            <CreateServerButton disabled={notConfigured} onTracked={(res) => trackAction(res, t("hetzner.ops.rebuild"))} />
          </div>
        }
      />

      {statusQ.data && <SafeModeBanner safeMode={statusQ.data.safe_mode} />}
      {notConfigured && <NotConfiguredBanner onGoSettings={() => navigate({ to: "/hetzner" })} />}
      <ActionStatusBanner
        actionId={actionId}
        label={actionLabel}
        onDone={() => {
          setActionId(null);
          ops.invalidate();
        }}
      />
      {confirmDialog}

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="servers">{t("hetzner.servers")}</TabsTrigger>
          <TabsTrigger value="snapshots">{t("hetzner.snapshots")}</TabsTrigger>
          <TabsTrigger value="policies">{t("hetzner.policies")}</TabsTrigger>
        </TabsList>

        <TabsContent value="servers" className="space-y-4 mt-4">
          {serversQ.isError && !isNotConfigured(serversQ.error) && (
            <LoadFailedBanner title={t("hetzner.listFailedTitle")} error={serversQ.error} onRetry={() => serversQ.refetch()} />
          )}
          {serversQ.isPending ? (
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-[220px] rounded-2xl" />
              ))}
            </div>
          ) : (serversQ.data || []).length === 0 ? (
            <Card>
              <EmptyState
                illustration="servers"
                title={t("hetzner.emptyServers")}
                description={t("hetzner.emptyServersDesc")}
              />
            </Card>
          ) : (
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
              {(serversQ.data || []).map((s) => (
                <ServerCard
                  key={s.id}
                  server={s}
                  safeMode={safeMode}
                  snapshots={snapshotsQ.data || []}
                  policies={policiesQ.data || []}
                  confirm={confirm}
                  ops={ops}
                  onTracked={trackAction}
                />
              ))}
            </div>
          )}

          {/* qB 节点配置:可选能力,默认收起 */}
          <details className="group rounded-2xl border border-border bg-card">
            <summary className="flex items-center justify-between gap-2 px-4 py-3 text-[13px] font-semibold list-none cursor-pointer [&::-webkit-details-marker]:hidden">
              <span className="flex items-center gap-2">
                <Database className="w-4 h-4 text-muted-foreground" />
                {t("hetzner.qb.title")}
              </span>
              <ChevronDown className="w-4 h-4 text-muted-foreground transition-transform group-open:rotate-180" />
            </summary>
            <div className="border-t border-border px-4 py-4">
              <QbSection servers={serversQ.data || []} />
            </div>
          </details>
        </TabsContent>

        <TabsContent value="snapshots" className="mt-4">
          <SnapshotsSection servers={serversQ.data || []} />
        </TabsContent>

        <TabsContent value="policies" className="mt-4">
          <PoliciesSection servers={serversQ.data || []} safeMode={safeMode} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

// ------------------------------------------------------------------ 创建服务器

function CreateServerButton({ disabled, onTracked }: { disabled: boolean; onTracked: (res: unknown) => void }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const create = useCreateHetznerServer();
  const [name, setName] = useState("");
  const [serverType, setServerType] = useState("");
  const [location, setLocation] = useState("");
  const [image, setImage] = useState("");
  const [sshKeys, setSshKeys] = useState("");
  const [ipv4, setIpv4] = useState("");
  const [ipv6, setIpv6] = useState("");

  const submit = async () => {
    const res = await create.mutateAsync({
      name: name.trim(),
      server_type: serverType.trim(),
      location: location.trim(),
      image: image.trim(),
      ssh_keys: sshKeys.split(",").map((s) => s.trim()).filter(Boolean) || undefined,
      primary_ipv4_id: ipv4.trim() || undefined,
      primary_ipv6_id: ipv6.trim() || undefined,
    });
    onTracked(res);
    setOpen(false);
  };

  return (
    <>
      <Button onClick={() => setOpen(true)} disabled={disabled}>
        <Plus className="w-4 h-4" />
        {t("hetzner.createServer")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t("hetzner.createServer")}</DialogTitle>
            <DialogDescription>{t("hetzner.serversDescription")}</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <Field label={t("hetzner.form.name")}>
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t("hetzner.form.namePlaceholder")} />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label={t("hetzner.form.serverType")}>
                <Input value={serverType} onChange={(e) => setServerType(e.target.value)} placeholder={t("hetzner.form.serverTypePlaceholder")} />
              </Field>
              <Field label={t("hetzner.form.location")}>
                <Input value={location} onChange={(e) => setLocation(e.target.value)} placeholder={t("hetzner.form.locationPlaceholder")} />
              </Field>
            </div>
            <Field label={t("hetzner.form.image")}>
              <Input value={image} onChange={(e) => setImage(e.target.value)} placeholder={t("hetzner.form.imagePlaceholder")} />
            </Field>
            <Field label={t("hetzner.form.sshKeys")}>
              <Input value={sshKeys} onChange={(e) => setSshKeys(e.target.value)} placeholder={t("hetzner.form.sshKeysPlaceholder")} />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label={t("hetzner.form.primaryIpv4")}>
                <Input value={ipv4} onChange={(e) => setIpv4(e.target.value)} placeholder={t("hetzner.form.primaryIpv4Placeholder")} />
              </Field>
              <Field label={t("hetzner.form.primaryIpv6")}>
                <Input value={ipv6} onChange={(e) => setIpv6(e.target.value)} placeholder={t("hetzner.form.primaryIpv6Placeholder")} />
              </Field>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>{t("hetzner.form.cancel")}</Button>
            <Button disabled={!name.trim() || !serverType.trim() || !location.trim() || !image.trim() || create.isPending} onClick={submit}>
              {t("hetzner.form.create")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="block text-[13px] font-medium mb-1.5">{label}</label>
      {children}
    </div>
  );
}

// ------------------------------------------------------------------ 服务器卡片

function ServerCard({
  server: s, safeMode, snapshots, policies, confirm, ops, onTracked,
}: {
  server: HetznerServer;
  safeMode: boolean;
  snapshots: HetznerSnapshot[];
  policies: HetznerPolicy[];
  confirm: ReturnType<typeof useConfirm>[0];
  ops: ReturnType<typeof useHetznerServerOp>;
  onTracked: (res: unknown, label: string) => void;
}) {
  const { t } = useTranslation();
  const [trafficOpen, setTrafficOpen] = useState(false);
  const [rebuildOpen, setRebuildOpen] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [snapshotOpen, setSnapshotOpen] = useState(false);
  const [policyOpen, setPolicyOpen] = useState(false);
  const [qbOpen, setQbOpen] = useState(false);
  // 重置密码后只显示一次的新密码(后端只在响应里返回一次)。
  const [resetPw, setResetPw] = useState<{ name: string; password: string } | null>(null);
  const policy = policies.find((p) => p.server_id === s.id);
  const mySnapshots = snapshots.filter((x) => x.server_id === s.id);

  const ask = async (opts: { title: string; desc: string; confirmText: string }) => {
    return confirm({ title: opts.title, description: opts.desc, confirmText: opts.confirmText, tone: "danger" });
  };

  return (
    <Card>
      <CardContent className="p-5 space-y-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <StatusDot tone={s.status === "running" ? "success" : s.status === "off" ? "danger" : "warning"} size="sm" pulse={s.status === "running"} />
              <h3 className="font-semibold text-[15px] truncate">{s.name}</h3>
              <Chip tone={s.status === "running" ? "success" : "default"}>{serverStatusLabel(s.status)}</Chip>
            </div>
            <p className="text-[12px] text-muted-foreground mt-1 truncate">
              #{s.id} · {s.server_type} · {s.location} · {t("hetzner.table.created")} {fmtDate(s.created)}
            </p>
            <p className="text-[12px] font-mono text-muted-foreground truncate mt-0.5">
              {s.ipv4 || "—"}{s.ipv6 ? ` · ${s.ipv6}` : ""}
            </p>
          </div>
          {policy?.enabled && (
            <Chip tone={safeMode ? "warning" : "success"}>
              <ShieldCheck className="w-3 h-3" />
              {t("hetzner.policy.enabled")} {Math.round(policy.threshold * 100)}%
            </Chip>
          )}
        </div>

        <TrafficBar traffic={s.traffic} />
        <p className="text-[11px] text-muted-foreground">
          {t("hetzner.qb.title")}: {s.qb_configured ? t("hetzner.qbConfigured") : t("hetzner.qbNotConfigured")}
          {mySnapshots.length > 0 ? ` · ${t("hetzner.snapshots")}: ${mySnapshots.length}` : ""}
        </p>

        <div className="flex flex-wrap gap-1.5">
          <Button
            size="sm" variant="outline"
            onClick={async () => {
              if (await ask({ title: t("hetzner.confirm.rebuildTitle"), desc: t("hetzner.confirm.rebuildDesc", { name: s.name }), confirmText: t("hetzner.confirm.confirmRebuild") })) {
                onTracked(await ops.rebuild(s.id), t("hetzner.ops.rebuild"));
              }
            }}
          >
            <RotateCcw className="w-3.5 h-3.5" />
            {t("hetzner.ops.rebuild")}
          </Button>
          <Button size="sm" variant="outline" onClick={() => setRebuildOpen(true)}>
            {t("hetzner.ops.rebuildFull")}
          </Button>
          <Button
            size="sm" variant="outline"
            onClick={async () => {
              if (await ask({ title: t("hetzner.confirm.rebootTitle"), desc: t("hetzner.confirm.rebootDesc", { name: s.name }), confirmText: t("hetzner.confirm.confirmReboot") })) {
                onTracked(await ops.reboot(s.id), t("hetzner.ops.reboot"));
              }
            }}
          >
            <Power className="w-3.5 h-3.5" />
            {t("hetzner.ops.reboot")}
          </Button>
          <Button
            size="sm" variant="outline"
            onClick={async () => {
              if (await ask({ title: t("hetzner.confirm.hardRebootTitle"), desc: t("hetzner.confirm.hardRebootDesc", { name: s.name }), confirmText: t("hetzner.confirm.confirmReboot") })) {
                onTracked(await ops.hardReboot(s.id), t("hetzner.ops.hardReboot"));
              }
            }}
          >
            <AlertTriangle className="w-3.5 h-3.5" />
            {t("hetzner.ops.hardReboot")}
          </Button>
          <Button
            size="sm" variant="outline"
            onClick={async () => {
              if (await ask({ title: t("hetzner.confirm.resetPasswordTitle"), desc: t("hetzner.confirm.resetPasswordDesc", { name: s.name }), confirmText: t("hetzner.confirm.confirmReset") })) {
                const res = await ops.resetPassword(s.id);
                onTracked(res, t("hetzner.ops.resetPassword"));
                const pw = (res as { data?: { new_password?: unknown } } | null)?.data?.new_password;
                if (typeof pw === "string" && pw) setResetPw({ name: s.name, password: pw });
              }
            }}
          >
            {t("hetzner.ops.resetPassword")}
          </Button>
          <Button size="sm" variant="outline" onClick={() => setSnapshotOpen(true)}>
            <Camera className="w-3.5 h-3.5" />
            {t("hetzner.ops.snapshot")}
          </Button>
          <Button size="sm" variant="outline" onClick={() => setPolicyOpen(true)}>
            <ShieldCheck className="w-3.5 h-3.5" />
            {t("hetzner.ops.policy")}
          </Button>
          <Button size="sm" variant="outline" onClick={() => setQbOpen(true)}>
            <Database className="w-3.5 h-3.5" />
            {t("hetzner.ops.qbConfig")}
          </Button>
          <Button size="sm" variant="outline" onClick={() => setRenameOpen(true)}>
            <Pencil className="w-3.5 h-3.5" />
            {t("hetzner.ops.rename")}
          </Button>
          <Button size="sm" variant="outline" onClick={() => setTrafficOpen(true)}>
            {t("hetzner.ops.details")}
          </Button>
          <Button size="sm" variant="destructive" onClick={() => setDeleteOpen(true)}>
            <Trash2 className="w-3.5 h-3.5" />
            {t("hetzner.ops.delete")}
          </Button>
        </div>
      </CardContent>

      <RebuildFromSnapshotDialog server={s} snapshots={mySnapshots} open={rebuildOpen} onOpenChange={setRebuildOpen} onTracked={onTracked} />
      <RenameDialog server={s} open={renameOpen} onOpenChange={setRenameOpen} />
      <DeleteServerDialog server={s} open={deleteOpen} onOpenChange={setDeleteOpen} onTracked={onTracked} />
      <CreateSnapshotDialog server={s} open={snapshotOpen} onOpenChange={setSnapshotOpen} onTracked={onTracked} />
      <PolicyDialog server={s} policy={policy} safeMode={safeMode} open={policyOpen} onOpenChange={setPolicyOpen} />
      <QbDialog server={s} open={qbOpen} onOpenChange={setQbOpen} />
      <TrafficDialog server={s} open={trafficOpen} onOpenChange={setTrafficOpen} />
      <ResetPasswordDialog data={resetPw} open={resetPw !== null} onOpenChange={(v) => { if (!v) setResetPw(null); }} />
    </Card>
  );
}

/** 重置密码后只显示一次的新密码弹窗(带复制按钮)。 */
function ResetPasswordDialog({ data, open, onOpenChange }: {
  data: { name: string; password: string } | null;
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    if (!data) return;
    try {
      await navigator.clipboard.writeText(data.password);
    } catch {
      const ta = document.createElement("textarea");
      ta.value = data.password;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      document.body.removeChild(ta);
    }
    setCopied(true);
    window.setTimeout(() => setCopied(false), 2000);
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("hetzner.resetPasswordDone.title")}</DialogTitle>
          <DialogDescription>{t("hetzner.resetPasswordDone.desc", { name: data?.name ?? "" })}</DialogDescription>
        </DialogHeader>
        <div>
          <label className="block text-[13px] font-medium mb-1.5">{t("hetzner.resetPasswordDone.newPassword")}</label>
          <div className="flex gap-2">
            <Input readOnly value={data?.password ?? ""} className="font-mono" onFocus={(e) => e.target.select()} />
            <Button variant="outline" onClick={copy}>
              <Copy className="w-3.5 h-3.5" />
              {copied ? t("hetzner.resetPasswordDone.copied") : t("hetzner.resetPasswordDone.copy")}
            </Button>
          </div>
        </div>
        <p className="text-[11px] text-muted-foreground">{t("hetzner.resetPasswordDone.onceNote")}</p>
        <DialogFooter>
          <Button onClick={() => onOpenChange(false)}>{t("hetzner.resetPasswordDone.close")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ------------------------------------------------------------------ 各操作弹窗

function RebuildFromSnapshotDialog({ server, snapshots, open, onOpenChange, onTracked }: {
  server: HetznerServer; snapshots: HetznerSnapshot[]; open: boolean; onOpenChange: (v: boolean) => void;
  onTracked: (res: unknown, label: string) => void;
}) {
  const { t } = useTranslation();
  const ops = useHetznerServerOp();
  const [imageId, setImageId] = useState("");
  const [confirm, confirmDialog] = useConfirm();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {confirmDialog}
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("hetzner.ops.rebuildFull")}</DialogTitle>
          <DialogDescription>{t("hetzner.confirm.rebuildFullDesc", { name: server.name })}</DialogDescription>
        </DialogHeader>
        <Field label={t("hetzner.form.snapshotId")}>
          <Select value={imageId} onValueChange={setImageId}>
            <SelectTrigger><SelectValue placeholder={t("hetzner.form.snapshotIdPlaceholder")} /></SelectTrigger>
            <SelectContent>
              {snapshots.map((sn) => (
                <SelectItem key={sn.id} value={String(sn.id)}>
                  #{sn.id} · {sn.description || sn.name || ""} ({formatBytes((sn.size_gb || 0) * 1024 ** 3)})
                </SelectItem>
              ))}
              {snapshots.length === 0 && <SelectItem value="" disabled>{t("hetzner.form.noSnapshots")}</SelectItem>}
            </SelectContent>
          </Select>
        </Field>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>{t("hetzner.form.cancel")}</Button>
          <Button
            variant="destructive"
            disabled={!imageId || ops.isPending}
            onClick={async () => {
              if (!(await confirm({ title: t("hetzner.confirm.rebuildFullTitle"), description: t("hetzner.confirm.rebuildFullDesc", { name: server.name }), confirmText: t("hetzner.confirm.confirmRebuild"), tone: "danger" }))) return;
              onTracked(await ops.rebuildFull(server.id, imageId), t("hetzner.ops.rebuildFull"));
              onOpenChange(false);
            }}
          >
            {t("hetzner.confirm.confirmRebuild")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function RenameDialog({ server, open, onOpenChange }: { server: HetznerServer; open: boolean; onOpenChange: (v: boolean) => void }) {
  const { t } = useTranslation();
  const rename = useRenameHetznerServer();
  const [name, setName] = useState(server.name);
  return (
    <Dialog open={open} onOpenChange={(v) => { if (v) setName(server.name); onOpenChange(v); }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader><DialogTitle>{t("hetzner.ops.rename")}</DialogTitle></DialogHeader>
        <Field label={t("hetzner.form.name")}>
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>{t("hetzner.form.cancel")}</Button>
          <Button
            disabled={!name.trim() || name.trim() === server.name || rename.isPending}
            onClick={() => { rename.mutate({ id: server.id, name: name.trim() }, { onSuccess: () => onOpenChange(false) }); }}
          >
            {t("hetzner.form.rename")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DeleteServerDialog({ server, open, onOpenChange, onTracked }: {
  server: HetznerServer; open: boolean; onOpenChange: (v: boolean) => void;
  onTracked: (res: unknown, label: string) => void;
}) {
  const { t } = useTranslation();
  const del = useDeleteHetznerServer();
  const [createSnapshot, setCreateSnapshot] = useState(true);
  const [keepIpv4, setKeepIpv4] = useState(true);
  const [keepIpv6, setKeepIpv6] = useState(true);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("hetzner.confirm.deleteTitle")}</DialogTitle>
          <DialogDescription>{t("hetzner.confirm.deleteDesc", { name: server.name })}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          {[
            { label: t("hetzner.form.createSnapshotBeforeDelete"), v: createSnapshot, set: setCreateSnapshot },
            { label: t("hetzner.form.keepIpv4"), v: keepIpv4, set: setKeepIpv4 },
            { label: t("hetzner.form.keepIpv6"), v: keepIpv6, set: setKeepIpv6 },
          ].map((row) => (
            <label key={row.label} className="flex items-center justify-between gap-3 text-[13px] cursor-pointer">
              {row.label}
              <Switch checked={row.v} onCheckedChange={row.set} />
            </label>
          ))}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>{t("hetzner.form.cancel")}</Button>
          <Button
            variant="destructive" disabled={del.isPending}
            onClick={async () => {
              const res = await del.mutateAsync({ id: server.id, create_snapshot: createSnapshot, keep_ipv4: keepIpv4, keep_ipv6: keepIpv6 });
              onTracked(res, t("hetzner.ops.delete"));
              onOpenChange(false);
            }}
          >
            {t("hetzner.confirm.confirmDelete")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function CreateSnapshotDialog({ server, open, onOpenChange, onTracked }: {
  server: HetznerServer; open: boolean; onOpenChange: (v: boolean) => void;
  onTracked: (res: unknown, label: string) => void;
}) {
  const { t } = useTranslation();
  const create = useCreateHetznerSnapshot();
  const estimateQ = useHetznerSnapshotEstimate(open ? server.id : null);
  const [description, setDescription] = useState("");
  return (
    <Dialog open={open} onOpenChange={(v) => { if (v) setDescription(""); onOpenChange(v); }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader><DialogTitle>{t("hetzner.snapshot.createTitle")}</DialogTitle></DialogHeader>
        <Field label={t("hetzner.form.description")}>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder={t("hetzner.form.descriptionPlaceholder")} />
        </Field>
        <div className="text-[12px] text-muted-foreground" role="status">
          {t("hetzner.snapshot.estimate")}:{" "}
          {estimateQ.isPending
            ? t("hetzner.snapshot.estimateLoading")
            : estimateQ.isError
              ? t("hetzner.snapshot.estimateFailed")
              : estimateQ.data
                ? t("hetzner.snapshot.estimateValue", { size: estimateQ.data.size_gb, price: estimateQ.data.price, currency: estimateQ.data.currency })
                : "—"}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>{t("hetzner.form.cancel")}</Button>
          <Button
            disabled={!description.trim() || create.isPending}
            onClick={async () => {
              const res = await create.mutateAsync({ server_id: server.id, description: description.trim() });
              onTracked(res, t("hetzner.ops.snapshot"));
              onOpenChange(false);
            }}
          >
            {t("hetzner.form.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PolicyDialog({ server, policy, safeMode, open, onOpenChange }: {
  server: HetznerServer; policy?: HetznerPolicy; safeMode: boolean; open: boolean; onOpenChange: (v: boolean) => void;
}) {
  const { t } = useTranslation();
  const save = useSaveHetznerPolicy();
  const [enabled, setEnabled] = useState(policy?.enabled ?? false);
  const [thresholdPct, setThresholdPct] = useState(policy ? String(Math.round(policy.threshold * 100)) : "90");
  const [imageId, setImageId] = useState(policy?.image_id ?? "");
  return (
    <Dialog open={open} onOpenChange={(v) => {
      if (v) {
        setEnabled(policy?.enabled ?? false);
        setThresholdPct(policy ? String(Math.round(policy.threshold * 100)) : "90");
        setImageId(policy?.image_id ?? "");
      }
      onOpenChange(v);
    }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader><DialogTitle>{t("hetzner.policy.createTitle")}</DialogTitle></DialogHeader>
        {safeMode && <p className="text-[12px] text-warning" role="status">{t("hetzner.policy.safeModeNote")}</p>}
        <div className="space-y-3">
          <label className="flex items-center justify-between gap-3 text-[13px] cursor-pointer">
            {t("hetzner.form.enabled")}
            <Switch checked={enabled} onCheckedChange={setEnabled} disabled={safeMode} aria-label={t("hetzner.form.enabled")} />
          </label>
          <Field label={`${t("hetzner.form.threshold")} (${t("hetzner.form.thresholdUnit")})`}>
            <Input
              type="number" min={1} max={100} value={thresholdPct}
              onChange={(e) => setThresholdPct(e.target.value)}
            />
            <p className="text-[11px] text-muted-foreground mt-1">{t("hetzner.form.thresholdHint")}</p>
          </Field>
          <Field label={t("hetzner.form.imageOptional")}>
            <Input value={imageId} onChange={(e) => setImageId(e.target.value)} placeholder={t("hetzner.form.imageIdPlaceholder")} />
          </Field>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>{t("hetzner.form.cancel")}</Button>
          <Button
            disabled={save.isPending || !imageId.trim()}
            onClick={() => {
              save.mutate(
                {
                  server_id: server.id,
                  enabled: safeMode ? false : enabled,
                  threshold: Math.min(100, Math.max(1, Number(thresholdPct) || 90)) / 100,
                  image_id: imageId.trim(),
                },
                { onSuccess: () => onOpenChange(false) },
              );
            }}
          >
            {t("hetzner.form.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function QbDialog({ server, open, onOpenChange }: { server: HetznerServer; open: boolean; onOpenChange: (v: boolean) => void }) {
  const { t } = useTranslation();
  const nodesQ = useHetznerQbNodes();
  const save = useSaveHetznerQbNode();
  const existing = (nodesQ.data || []).find((n) => n.server_id === server.id);
  const [url, setUrl] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  return (
    <Dialog open={open} onOpenChange={(v) => {
      if (v) {
        setUrl(existing?.url ?? "");
        setUsername(existing?.username ?? "");
        setPassword("");
      }
      onOpenChange(v);
    }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("hetzner.qb.editTitle")}</DialogTitle>
          <DialogDescription>{server.name} (#{server.id})</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <Field label={t("hetzner.form.qbUrl")}>
            <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder={t("hetzner.form.qbUrlPlaceholder")} />
          </Field>
          <Field label={t("hetzner.form.qbUsername")}>
            <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
          </Field>
          <Field label={t("hetzner.form.qbPassword")}>
            <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
          </Field>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>{t("hetzner.form.cancel")}</Button>
          <Button
            disabled={!url.trim() || save.isPending}
            onClick={() => {
              save.mutate(
                { server_id: server.id, url: url.trim(), username: username.trim(), password },
                { onSuccess: () => onOpenChange(false) },
              );
            }}
          >
            {t("hetzner.form.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function TrafficDialog({ server, open, onOpenChange }: { server: HetznerServer; open: boolean; onOpenChange: (v: boolean) => void }) {
  const { t } = useTranslation();
  const trafficQ = useHetznerTraffic(open ? server.id : null, 30);
  const days = useMemo(
    () => (trafficQ.data?.days || []).map((d) => ({ ...d, label: fmtDate(d.date) })),
    [trafficQ.data],
  );
  const totalOut = days.reduce((a, d) => a + d.outbound_bytes, 0);
  const totalIn = days.reduce((a, d) => a + d.inbound_bytes, 0);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="w-[95vw] sm:w-full sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("hetzner.detail.title")} · {server.name}</DialogTitle>
          <DialogDescription>
            {t("hetzner.detail.totalOut")}: {formatBytes(totalOut)} · {t("hetzner.detail.totalIn")}: {formatBytes(totalIn)}
          </DialogDescription>
        </DialogHeader>
        {trafficQ.isPending ? (
          <Skeleton className="h-[220px] rounded-xl" />
        ) : trafficQ.isError ? (
          <LoadFailed compact title={t("hetzner.listFailedTitle")} error={trafficQ.error} onRetry={() => trafficQ.refetch()} />
        ) : days.length === 0 ? (
          <EmptyState illustration="search" title={t("hetzner.chartEmpty")} description="" />
        ) : (
          <div className="h-[240px] w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={days} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
                <CartesianGrid strokeDasharray="3 3" opacity={0.3} />
                <XAxis dataKey="label" tick={{ fontSize: 11 }} tickMargin={8} minTickGap={32} />
                <YAxis tick={{ fontSize: 11 }} width={64} tickFormatter={(v: number) => formatBytes(v)} />
                <Tooltip
                  formatter={(v, name) => [
                    formatBytes(Number(v)),
                    name === "outbound_bytes" ? t("hetzner.chartOutbound") : t("hetzner.chartInbound"),
                  ]}
                />
                <Area type="monotone" dataKey="outbound_bytes" stroke="var(--primary)" fill="var(--primary)" fillOpacity={0.15} strokeWidth={2} />
                <Area type="monotone" dataKey="inbound_bytes" stroke="var(--warning, #eab308)" fill="var(--warning, #eab308)" fillOpacity={0.12} strokeWidth={2} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ------------------------------------------------------------------ 快照页

function SnapshotsSection({ servers }: { servers: HetznerServer[] }) {
  const { t } = useTranslation();
  const snapshotsQ = useHetznerSnapshots();
  const del = useDeleteHetznerSnapshot();
  const [confirm, confirmDialog] = useConfirm();
  const [renameTarget, setRenameTarget] = useState<HetznerSnapshot | null>(null);
  const [createServerId, setCreateServerId] = useState<string>("");
  const serverName = (id: number) => servers.find((s) => s.id === id)?.name || `#${id}`;

  return (
    <div className="space-y-4">
      {confirmDialog}
      <Card>
        <CardContent className="p-5 space-y-3">
          <h3 className="text-sm font-semibold">{t("hetzner.snapshot.title")}</h3>
          {snapshotsQ.isPending ? (
            <Skeleton className="h-[120px] rounded-xl" />
          ) : snapshotsQ.isError ? (
            <LoadFailed compact title={t("hetzner.listFailedTitle")} error={snapshotsQ.error} onRetry={() => snapshotsQ.refetch()} />
          ) : (snapshotsQ.data || []).length === 0 ? (
            <EmptyState illustration="search" title={t("hetzner.snapshot.empty")} description={t("hetzner.snapshot.emptyDesc")} />
          ) : (
            <div className="divide-y divide-border">
              {(snapshotsQ.data || []).map((sn) => (
                <div key={sn.id} className="flex items-center gap-3 py-3 first:pt-0 last:pb-0 flex-wrap">
                  <Camera className="w-4 h-4 text-muted-foreground flex-shrink-0" />
                  <div className="min-w-0 flex-1">
                    <p className="text-[13px] font-medium truncate">#{sn.id} · {sn.description || sn.name || "—"}</p>
                    <p className="text-[11px] text-muted-foreground">
                      {t("hetzner.snapshot.server")}: {serverName(sn.server_id)} · {t("hetzner.snapshot.size")}: {formatBytes((sn.size_gb || 0) * 1024 ** 3)}
                      {sn.created ? ` · ${fmtDateTime(sn.created)}` : ""}
                    </p>
                  </div>
                  <Button size="sm" variant="outline" onClick={() => setRenameTarget(sn)}>
                    <Pencil className="w-3.5 h-3.5" />
                    {t("hetzner.ops.rename")}
                  </Button>
                  <Button
                    size="sm" variant="destructive"
                    disabled={del.isPending}
                    onClick={async () => {
                      if (await confirm({
                        title: t("hetzner.confirm.deleteSnapshotTitle"),
                        description: t("hetzner.confirm.deleteSnapshotDesc", { name: sn.description || `#${sn.id}` }),
                        confirmText: t("hetzner.confirm.confirmDelete"),
                        tone: "danger",
                      })) {
                        del.mutate(sn.id);
                      }
                    }}
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                    {t("hetzner.ops.delete")}
                  </Button>
                </div>
              ))}
            </div>
          )}
          <div className="flex items-center gap-2 flex-wrap pt-1">
            <Select value={createServerId} onValueChange={setCreateServerId}>
              <SelectTrigger className="w-52">
                <SelectValue placeholder={t("hetzner.snapshot.server")} />
              </SelectTrigger>
              <SelectContent>
                {servers.map((s) => (
                  <SelectItem key={s.id} value={String(s.id)}>{s.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <CreateSnapshotInline serverId={createServerId ? Number(createServerId) : null} />
          </div>
        </CardContent>
      </Card>
      {renameTarget && (
        <SnapshotRenameDialog snapshot={renameTarget} onClose={() => setRenameTarget(null)} />
      )}
    </div>
  );
}

function CreateSnapshotInline({ serverId }: { serverId: number | null }) {
  const { t } = useTranslation();
  const create = useCreateHetznerSnapshot();
  const [description, setDescription] = useState("");
  const estimateQ = useHetznerSnapshotEstimate(serverId);
  return (
    <div className="flex items-center gap-2 flex-wrap flex-1">
      <Input
        className="w-52"
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        placeholder={t("hetzner.form.descriptionPlaceholder")}
      />
      <Button
        size="sm"
        disabled={!serverId || !description.trim() || create.isPending}
        onClick={() => {
          if (!serverId) return;
          create.mutate({ server_id: serverId, description: description.trim() }, { onSuccess: () => setDescription("") });
        }}
      >
        <Camera className="w-3.5 h-3.5" />
        {t("hetzner.snapshot.createTitle")}
      </Button>
      {serverId !== null && estimateQ.data && (
        <span className="text-[11px] text-muted-foreground">
          {t("hetzner.snapshot.estimateValue", { size: estimateQ.data.size_gb, price: estimateQ.data.price, currency: estimateQ.data.currency })}
        </span>
      )}
    </div>
  );
}

function SnapshotRenameDialog({ snapshot, onClose }: { snapshot: HetznerSnapshot; onClose: () => void }) {
  const { t } = useTranslation();
  const rename = useRenameHetznerSnapshot();
  const [description, setDescription] = useState(snapshot.description || "");
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader><DialogTitle>{t("hetzner.snapshot.renameTitle")}</DialogTitle></DialogHeader>
        <Field label={t("hetzner.form.description")}>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>{t("hetzner.form.cancel")}</Button>
          <Button
            disabled={!description.trim() || rename.isPending}
            onClick={() => rename.mutate({ id: snapshot.id, description: description.trim() }, { onSuccess: onClose })}
          >
            {t("hetzner.form.rename")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ------------------------------------------------------------------ 策略页

function PoliciesSection({ servers, safeMode }: { servers: HetznerServer[]; safeMode: boolean }) {
  const { t } = useTranslation();
  const policiesQ = useHetznerPolicies();
  const del = useDeleteHetznerPolicy();
  const [confirm, confirmDialog] = useConfirm();
  const [editTarget, setEditTarget] = useState<HetznerServer | null>(null);
  const serverName = (id: number) => servers.find((s) => s.id === id)?.name || `#${id}`;

  return (
    <div className="space-y-4">
      {confirmDialog}
      {safeMode && (
        <p className="text-[12px] text-warning rounded-xl border border-warning/40 bg-warning/5 px-3 py-2" role="status">
          {t("hetzner.policy.safeModeNote")}
        </p>
      )}
      <Card>
        <CardContent className="p-5 space-y-3">
          <h3 className="text-sm font-semibold">{t("hetzner.policy.title")}</h3>
          {policiesQ.isPending ? (
            <Skeleton className="h-[120px] rounded-xl" />
          ) : policiesQ.isError ? (
            <LoadFailed compact title={t("hetzner.listFailedTitle")} error={policiesQ.error} onRetry={() => policiesQ.refetch()} />
          ) : (policiesQ.data || []).length === 0 ? (
            <EmptyState illustration="search" title={t("hetzner.policy.empty")} description={t("hetzner.policy.emptyDesc")} />
          ) : (
            <div className="divide-y divide-border">
              {(policiesQ.data || []).map((p) => (
                <div key={p.server_id} className="flex items-center gap-3 py-3 first:pt-0 last:pb-0 flex-wrap">
                  <ShieldCheck className="w-4 h-4 text-muted-foreground flex-shrink-0" />
                  <div className="min-w-0 flex-1">
                    <p className="text-[13px] font-medium truncate">{serverName(p.server_id)}</p>
                    <p className="text-[11px] text-muted-foreground">
                      {t("hetzner.policy.threshold")}: {Math.round(p.threshold * 100)}% · {t("hetzner.policy.image")}: {p.image_id || "—"}
                    </p>
                  </div>
                  <Chip tone={p.enabled ? "success" : "default"}>
                    {p.enabled ? t("hetzner.policy.enabled") : t("hetzner.policy.disabled")}
                  </Chip>
                  <Button
                    size="sm" variant="outline"
                    onClick={() => {
                      const s = servers.find((x) => x.id === p.server_id);
                      if (s) setEditTarget(s);
                    }}
                  >
                    <Pencil className="w-3.5 h-3.5" />
                    {t("common.edit")}
                  </Button>
                  <Button
                    size="sm" variant="destructive" disabled={del.isPending}
                    onClick={async () => {
                      if (await confirm({
                        title: t("hetzner.confirm.deletePolicyTitle"),
                        description: t("hetzner.confirm.deletePolicyDesc", { name: serverName(p.server_id) }),
                        confirmText: t("hetzner.confirm.confirmDelete"),
                        tone: "danger",
                      })) {
                        del.mutate(p.server_id);
                      }
                    }}
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                    {t("hetzner.ops.delete")}
                  </Button>
                </div>
              ))}
            </div>
          )}
          <div className="flex items-center gap-2 flex-wrap pt-1">
            <span className="text-[12px] text-muted-foreground">{t("hetzner.policy.createTitle")}:</span>
            {servers.map((s) => (
              <Button key={s.id} size="sm" variant="outline" onClick={() => setEditTarget(s)}>
                {s.name}
              </Button>
            ))}
          </div>
        </CardContent>
      </Card>
      {editTarget && (
        <PolicyDialog
          server={editTarget}
          policy={(policiesQ.data || []).find((p) => p.server_id === editTarget.id)}
          safeMode={safeMode}
          open
          onOpenChange={(v) => !v && setEditTarget(null)}
        />
      )}
    </div>
  );
}

// ------------------------------------------------------------------ qB 节点

function QbSection({ servers }: { servers: HetznerServer[] }) {
  const { t } = useTranslation();
  const nodesQ = useHetznerQbNodes();
  const statusQ = useHetznerQbStatus();
  const del = useDeleteHetznerQbNode();
  const [confirm, confirmDialog] = useConfirm();
  const [editServer, setEditServer] = useState<HetznerServer | null>(null);
  const statusByServer = new Map((statusQ.data || []).map((s) => [s.server_id, s]));
  const serverName = (id: number) => servers.find((s) => s.id === id)?.name || `#${id}`;

  return (
    <div className="space-y-3">
      {confirmDialog}
      <p className="text-[12px] text-muted-foreground">{t("hetzner.qb.desc")}</p>
      {statusQ.isError && (
        <LoadFailedBanner title={t("hetzner.qb.statusFailedTitle")} error={statusQ.error} onRetry={() => statusQ.refetch()} />
      )}
      {nodesQ.isPending ? (
        <Skeleton className="h-[100px] rounded-xl" />
      ) : (nodesQ.data || []).length === 0 ? (
        <EmptyState illustration="search" title={t("hetzner.qb.empty")} description={t("hetzner.qb.emptyDesc")} />
      ) : (
        <div className="divide-y divide-border">
          {(nodesQ.data || []).map((n) => {
            const st = statusByServer.get(n.server_id);
            return (
              <div key={n.server_id} className="flex items-center gap-3 py-3 first:pt-0 last:pb-0 flex-wrap">
                <Database className="w-4 h-4 text-muted-foreground flex-shrink-0" />
                <div className="min-w-0 flex-1">
                  <p className="text-[13px] font-medium truncate">{serverName(n.server_id)} · {n.url}</p>
                  <p className="text-[11px] text-muted-foreground tabular-nums">
                    {st ? (
                      <>
                        {t("hetzner.qb.status")}: {st.connection_status || "—"}
                        {` · ↑ ${formatBytesPerSec(st.up_speed || 0)} · ↓ ${formatBytesPerSec(st.dl_speed || 0)}`}
                        {` · ${t("hetzner.qb.torrents")}: ${st.active_torrents ?? 0}/${st.all_torrents ?? 0}`}
                        {` · ${t("hetzner.qb.totalUp")}: ${formatBytes(st.up_total || 0)} · ${t("hetzner.qb.totalDown")}: ${formatBytes(st.dl_total || 0)}`}
                      </>
                    ) : (
                      t("hetzner.qbNotConfigured")
                    )}
                  </p>
                </div>
                <Button
                  size="sm" variant="outline"
                  onClick={() => {
                    const s = servers.find((x) => x.id === n.server_id);
                    if (s) setEditServer(s);
                  }}
                >
                  {t("hetzner.qb.configure")}
                </Button>
                <Button
                  size="sm" variant="destructive" disabled={del.isPending}
                  onClick={async () => {
                    if (await confirm({
                      title: t("hetzner.confirm.deleteQbTitle"),
                      description: t("hetzner.confirm.deleteQbDesc", { name: serverName(n.server_id) }),
                      confirmText: t("hetzner.confirm.confirmDelete"),
                      tone: "danger",
                    })) {
                      del.mutate(n.server_id);
                    }
                  }}
                >
                  <Trash2 className="w-3.5 h-3.5" />
                  {t("hetzner.ops.delete")}
                </Button>
              </div>
            );
          })}
        </div>
      )}
      <div className="flex items-center gap-2 flex-wrap pt-1">
        <span className="text-[12px] text-muted-foreground">{t("hetzner.qb.configure")}:</span>
        {servers.map((s) => (
          <Button key={s.id} size="sm" variant="outline" onClick={() => setEditServer(s)}>
            {s.name}
          </Button>
        ))}
      </div>
      {editServer && <QbDialog server={editServer} open onOpenChange={(v) => !v && setEditServer(null)} />}
    </div>
  );
}
