// ============================================================================
// Cloud Runtime 对话框（CloudRuntimeDialog）——在云上开一台托管 runtime 节点
//
// 角色定位：SaaS 版 runtimes 页「Cloud Runtime」入口打开的弹窗，左半边是
// 创建表单（名称 / 实例规格 / 磁盘大小），右半边是现有云端节点列表（可手动
// 刷新、可删除）。
//
// 数据流：列表数据来自 cloudRuntimeNodeListOptions（只取最近 20 条，按创建
// 时间倒序展示）；创建 / 删除都走 @multica/core 的 mutation，成功后弹 toast。
// 与自托管 daemon 无关——这台节点由 multica 官方云托管，不占用户自己的机器。
// ============================================================================
"use client";

import { useId, useMemo, useState } from "react";
import type { FormEvent, HTMLAttributes } from "react";
import { useQuery } from "@tanstack/react-query";
import { Cloud, Loader2, RefreshCw, Rocket, Trash2 } from "lucide-react";
import { toast } from "sonner";
import type { CloudRuntimeNode } from "@multica/core/runtimes";
import {
  cloudRuntimeNodeListOptions,
  useCreateCloudRuntimeNode,
  useDeleteCloudRuntimeNode,
} from "@multica/core/runtimes";
import { useWorkspaceId } from "@multica/core/hooks";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

// 云端节点可选的 EC2 实例规格（AWS t4g 系列），下拉框选项的唯一事实来源。
// 加规格时同步改这里即可，表单与校验都自动跟随。
const CLOUD_RUNTIME_INSTANCE_TYPES = ["t4g.medium", "t4g.large"] as const;
// 默认选第一个规格；默认磁盘 20GB（表单留空时也用这个值兜底）。
const DEFAULT_INSTANCE_TYPE = CLOUD_RUNTIME_INSTANCE_TYPES[0];
const DEFAULT_DISK_SIZE_GB = 20;

/**
 * Cloud Runtime 对话框：创建并管理多台云端托管的 runtime 节点。
 *
 * @param onClose - 请求关闭对话框。
 *
 * 布局左右分栏：左侧创建表单、右侧节点列表。提交成功不自动关弹窗，而是清空
 * 表单让用户连续创建；新节点出现在右侧列表，状态由后端驱动。
 */
export function CloudRuntimeDialog({ onClose }: { onClose: () => void }) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const idPrefix = `cloud-runtime-${useId().replace(/:/g, "")}`;
  const formId = `${idPrefix}-form`;
  const [name, setName] = useState("");
  const [instanceType, setInstanceType] = useState<string>(
    DEFAULT_INSTANCE_TYPE,
  );
  const [diskSizeGB, setDiskSizeGB] = useState(String(DEFAULT_DISK_SIZE_GB));

  const nodesQuery = useQuery(
    cloudRuntimeNodeListOptions(wsId, { limit: 20, offset: 0 }),
  );
  const createNode = useCreateCloudRuntimeNode(wsId);

  // 按创建时间倒序排，最新开的在最上面最显眼——无论接口返回什么顺序，这里
  // 都显式排一遍，保证 UI 顺序稳定。
  const sortedNodes = useMemo(
    () =>
      (nodesQuery.data ?? []).toSorted(
        (a, b) =>
          new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
      ),
    [nodesQuery.data],
  );

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    // 磁盘大小留空时用默认值；填了就要求是正整数（允许 0 没有意义，
    // 小数盘在云厂商那里也建不出来）。
    const diskSize = diskSizeGB.trim()
      ? Number(diskSizeGB.trim())
      : DEFAULT_DISK_SIZE_GB;
    if (!Number.isInteger(diskSize) || diskSize <= 0) {
      toast.error(t(($) => $.cloud_runtime.validation.disk_size_invalid));
      return;
    }

    try {
      await createNode.mutateAsync({
        instance_type: instanceType,
        name: valueOrUndefined(name),
        disk_size_gb: diskSize,
      });
      toast.success(t(($) => $.cloud_runtime.toast_created));
      setName("");
      setInstanceType(DEFAULT_INSTANCE_TYPE);
      setDiskSizeGB(String(DEFAULT_DISK_SIZE_GB));
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.cloud_runtime.toast_create_failed),
      );
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="flex max-h-[88vh] flex-col gap-0 p-0 sm:max-w-3xl">
        <DialogHeader className="border-b px-6 py-5">
          <DialogTitle className="flex items-center gap-2 text-title-sm">
            <Cloud className="h-4 w-4 text-muted-foreground" />
            {t(($) => $.cloud_runtime.title)}
          </DialogTitle>
          <DialogDescription className="text-caption">
            {t(($) => $.cloud_runtime.description)}
          </DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">
          <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(280px,0.82fr)]">
            <form id={formId} onSubmit={handleSubmit} className="space-y-4">
              <div>
                <h3 className="text-body font-medium">
                  {t(($) => $.cloud_runtime.create_title)}
                </h3>
                <p className="mt-1 text-caption text-muted-foreground">
                  {t(($) => $.cloud_runtime.create_hint)}
                </p>
              </div>

              <div className="grid gap-3 sm:grid-cols-2">
                <LabeledInput
                  id={`${idPrefix}-name`}
                  label={t(($) => $.cloud_runtime.fields.name)}
                  value={name}
                  onChange={setName}
                  placeholder={t(($) => $.cloud_runtime.placeholders.name)}
                />
                <LabeledInput
                  id={`${idPrefix}-instance-type`}
                  label={t(($) => $.cloud_runtime.fields.instance_type)}
                  value={instanceType}
                  onChange={setInstanceType}
                  options={CLOUD_RUNTIME_INSTANCE_TYPES}
                />
                <LabeledInput
                  id={`${idPrefix}-disk-size`}
                  label={t(($) => $.cloud_runtime.fields.disk_size)}
                  value={diskSizeGB}
                  onChange={setDiskSizeGB}
                  placeholder={String(DEFAULT_DISK_SIZE_GB)}
                  type="number"
                  inputMode="numeric"
                />
              </div>
            </form>

            <section className="min-h-0 rounded-md border bg-muted/20">
              <div className="flex items-center justify-between border-b bg-background px-3 py-2.5">
                <h3 className="text-body font-medium">
                  {t(($) => $.cloud_runtime.nodes_title)}
                </h3>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => void nodesQuery.refetch()}
                  disabled={nodesQuery.isFetching}
                  className="h-7 px-2"
                >
                  <RefreshCw
                    className={cn(
                      "h-3.5 w-3.5",
                      nodesQuery.isFetching && "animate-spin",
                    )}
                  />
                  {t(($) => $.cloud_runtime.refresh)}
                </Button>
              </div>

              {nodesQuery.isLoading ? (
                <div className="flex h-40 items-center justify-center">
                  <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
                </div>
              ) : nodesQuery.isError ? (
                <div className="flex h-40 flex-col items-center justify-center px-5 text-center">
                  <p className="text-body font-medium">
                    {t(($) => $.cloud_runtime.nodes_failed)}
                  </p>
                  <p className="mt-1 text-caption text-muted-foreground">
                    {nodesQuery.error instanceof Error
                      ? nodesQuery.error.message
                      : t(($) => $.cloud_runtime.nodes_failed_hint)}
                  </p>
                </div>
              ) : sortedNodes.length === 0 ? (
                <div className="flex h-40 flex-col items-center justify-center px-5 text-center">
                  <Cloud className="h-7 w-7 text-faint-foreground" />
                  <p className="mt-3 text-body font-medium">
                    {t(($) => $.cloud_runtime.nodes_empty)}
                  </p>
                </div>
              ) : (
                <div className="max-h-[410px] overflow-y-auto p-2">
                  <div className="space-y-2">
                    {sortedNodes.map((node) => (
                      <CloudRuntimeNodeRow key={node.id} node={node} wsId={wsId} />
                    ))}
                  </div>
                </div>
              )}
            </section>
          </div>
        </div>

        <DialogFooter className="m-0 border-t bg-muted/30 px-6 py-3">
          <Button type="button" variant="outline" size="sm" onClick={onClose}>
            {t(($) => $.cloud_runtime.cancel)}
          </Button>
          <Button
            type="submit"
            size="sm"
            form={formId}
            disabled={createNode.isPending}
          >
            {createNode.isPending ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Rocket className="h-3.5 w-3.5" />
            )}
            {createNode.isPending
              ? t(($) => $.cloud_runtime.creating)
              : t(($) => $.cloud_runtime.create)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// 带 label 的表单项：传 options 时渲染成下拉框（Select），否则渲染成输入框。
// 是「实例规格用下拉、名称 / 磁盘用输入」这一差异的统一封装。
function LabeledInput({
  id,
  label,
  value,
  onChange,
  placeholder,
  required,
  type = "text",
  inputMode,
  options,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  required?: boolean;
  type?: string;
  inputMode?: HTMLAttributes<HTMLInputElement>["inputMode"];
  options?: readonly string[];
}) {
  if (options) {
    return (
      <div className="space-y-1.5">
        <Label htmlFor={id} className="text-caption text-muted-foreground">
          {label}
        </Label>
        <Select
          items={options.map((option) => ({ value: option, label: option }))}
          value={value}
          onValueChange={(next) => onChange(next ?? value)}
        >
          <SelectTrigger id={id} className="h-9 w-full rounded-md text-body">
            <SelectValue>
              {() => <span className="truncate">{value}</span>}
            </SelectValue>
          </SelectTrigger>
          <SelectContent align="start">
            {options.map((option) => (
              <SelectItem key={option} value={option}>
                {option}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    );
  }

  return (
    <div className="space-y-1.5">
      <Label htmlFor={id} className="text-caption text-muted-foreground">
        {label}
      </Label>
      <Input
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        required={required}
        type={type}
        inputMode={inputMode}
        className="h-9 text-body"
      />
    </div>
  );
}

// 右侧列表里的一行节点：展示标题（名称或实例 id，都没有才用兜底文案）、
// 规格 / 区域 / 创建时间，以及一个带原生 confirm 的删除按钮。
function CloudRuntimeNodeRow({ node, wsId }: { node: CloudRuntimeNode; wsId: string }) {
  const { t } = useT("runtimes");
  const deleteNode = useDeleteCloudRuntimeNode(wsId);
  const title =
    node.name.trim() ||
    node.instance_id.trim() ||
    t(($) => $.cloud_runtime.node_fallback_name);
  const created = formatDateTime(node.created_at);
  return (
    <div className="rounded-md border bg-background px-3 py-2.5">
      <div className="flex min-w-0 items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate text-body font-medium">{title}</span>
            <CloudRuntimeStatusBadge status={node.status} />
          </div>
          <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-caption text-muted-foreground">
            <span>{node.instance_type}</span>
            <span className="text-faint-foreground">/</span>
            <span>{node.region}</span>
            {created && (
              <>
                <span className="text-faint-foreground">/</span>
                <span>{created}</span>
              </>
            )}
          </div>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-7 w-7 shrink-0 p-0 text-muted-foreground hover:text-destructive"
          disabled={deleteNode.isPending}
          onClick={() => {
            if (!confirm(t(($) => $.cloud_runtime.delete_confirm))) return;
            deleteNode.mutate(node.instance_id, {
              onSuccess: () => toast.success(t(($) => $.cloud_runtime.toast_deleted)),
              onError: (err) =>
                toast.error(
                  err instanceof Error
                    ? err.message
                    : t(($) => $.cloud_runtime.toast_delete_failed),
                ),
            });
          }}
          aria-label={t(($) => $.cloud_runtime.delete)}
        >
          {deleteNode.isPending ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Trash2 className="h-3.5 w-3.5" />
          )}
        </Button>
      </div>
      {node.instance_id && (
        <div className="mt-2 truncate font-mono text-micro text-muted-foreground">
          {node.instance_id}
        </div>
      )}
    </div>
  );
}

// 把后端返回的状态字符串归类成「正常 / 进行中 / 失败」三档，分别染成
// 绿 / 黄 / 红。归类是白名单式的：不认识的新状态不做任何染色，灰底展示原文，
// 避免把未知状态误标成成功或失败。
function CloudRuntimeStatusBadge({ status }: { status: string }) {
  const normalized = status.toLowerCase();
  const active = new Set(["running", "success"]);
  const pending = new Set([
    "launching",
    "pending",
    "starting",
    "stopping",
    "rebooting",
    "terminating",
  ]);
  const failed = new Set(["failed", "terminated", "error"]);
  return (
    <Badge
      variant="secondary"
      className={cn(
        "h-5 rounded-md px-1.5 font-mono text-micro",
        active.has(normalized) && "bg-success/10 text-success",
        pending.has(normalized) && "bg-warning/10 text-warning",
        failed.has(normalized) && "bg-destructive/10 text-destructive",
      )}
    >
      {status || "unknown"}
    </Badge>
  );
}

// 名称可留空：留空时转成 undefined 而不是空串发给后端——undefined 表示
// 「未提供名字」，语义明确，避免把一个看起来合法的空名字存进去。
function valueOrUndefined(value: string): string | undefined {
  const trimmed = value.trim();
  return trimmed ? trimmed : undefined;
}

// 把 ISO 时间串格式化成「MMM d, HH:mm」给 UI 展示；解析失败（如空串或非法
// 日期）返回 null，由调用方决定不显示时间，而不是把 "Invalid Date" 露出来。
function formatDateTime(value: string): string | null {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}
