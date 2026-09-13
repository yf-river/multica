// 本文件是「快捷操作」设置标签页（MUL-5465）：管理工作区里预置的「快捷操作」——
// 一段绑定到某个 agent/小组的固定提示词，供用户一键复用。支持新建、编辑、搜索、
// 归档/还原与删除。

"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Archive,
  ArchiveRestore,
  Globe,
  Info,
  Lock,
  MoreHorizontal,
  Pencil,
  Plus,
  Search,
  Trash2,
  TriangleAlert,
  Zap,
} from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  quickActionListOptions,
  useCreateQuickAction,
  useDeleteQuickAction,
  useUpdateQuickAction,
} from "@multica/core/quick-actions";
import type {
  QuickAction,
  QuickActionAssigneeType,
  QuickActionVisibility,
} from "@multica/core/types";
import { findQuickActionTemplateToken } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Label as FieldLabel } from "@multica/ui/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";
import { AgentPicker } from "../../autopilots/components/pickers/agent-picker";
import { useLocale, useT } from "../../i18n";
import { SettingsTab } from "./settings-layout";

// 排序按 use_count 降序而非创建时间：列表的职责是回答「这个工作区到底在用哪些」，
// 创建早晚与这无关。从没被用过的行自然沉底——正好也是「没被用」的清理信号该出现的位置。
//
// 布局刻意对齐 Labels 与 Properties 两个标签页：同样的搜索 + 主操作行、同样的描边
// 卡片、同样的在 `md` 断点下收成堆叠行的响应式列栅格、同样的 ⋯ 溢出菜单。这三个页
// 都是工作区「一筐有名字的小物件」目录，读起来像同一块表面，比各自耍花样重要。

/** 未使用达到该天数（90 天）就标成「久未使用」。 */
const UNUSED_DAYS_THRESHOLD = 90;

/**
 * 是否算「久未使用」。判断前提是给这个数字留出被使用的时间：新建的快捷操作天然是
 * 「从未用过」，若第一天就标黄，人们会习惯性无视这个颜色——信号必须表达「闲置很久」
 * 而非「这是新的」。优先按最近使用时间算，从未用过则回退到「创建至今已多久」。
 */
function isStale(action: QuickAction): boolean {
  const sinceLastUse = daysSince(action.last_used_at);
  if (sinceLastUse !== null) return sinceLastUse >= UNUSED_DAYS_THRESHOLD;
  const age = daysSince(action.created_at);
  return age !== null && age >= UNUSED_DAYS_THRESHOLD;
}

/** 距今过去的天数；入参为空或解析失败返回 null。 */
function daysSince(iso: string | null): number | null {
  if (!iso) return null;
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return null;
  return Math.floor((Date.now() - then) / 86_400_000);
}

/** 取 useT 的翻译函数类型，供无组件上下文的小组件复用。 */
type QuickActionsT = ReturnType<typeof useT<"settings">>["t"];

/**
 * 可见性角标（公开 / 私有）。visibility 是服务端下发的枚举：遇到不认识的新值时
 * 落到通用兜底（直接显示原值），不能渲染成空白（API 兼容规则）。
 */
function VisibilityBadge({ action, t }: { action: QuickAction; t: QuickActionsT }) {
  switch (action.visibility) {
    case "public":
      return (
        <Badge variant="secondary" className="gap-1 font-normal">
          <Globe className="size-3" />
          {t(($) => $.quick_actions.visibility_public)}
        </Badge>
      );
    case "private":
      return (
        <Badge variant="outline" className="gap-1 font-normal">
          <Lock className="size-3" />
          {t(($) => $.quick_actions.visibility_private)}
        </Badge>
      );
    default:
      return (
        <Badge variant="outline" className="font-normal">
          {action.visibility}
        </Badge>
      );
  }
}

/**
 * 目标行：展示绑定的 agent 及其「当前是否可被调用」的元信息。
 *
 * 有意用这种纯元数据表达替代派生的「失效」布尔：一个公开动作若其 agent 后来转成
 * 私有，这里读起来自然就是「公开角标 + 私有目标」，无需维护一套专属的错误态。
 * 代价是谁都不会被主动通知——这是刻意取舍（漂移少见，且真到点击时才暴露、不是
 * 悄悄失败）。
 */
function TargetLine({ action, t }: { action: QuickAction; t: QuickActionsT }) {
  if (action.target_missing === true) {
    return (
      <span className="inline-flex items-center gap-1 text-destructive">
        <TriangleAlert className="size-3" />
        {t(($) => $.quick_actions.target_missing)}
      </span>
    );
  }
  const mismatched = action.visibility === "public" && action.target_public !== true;
  return (
    <span className={cn("inline-flex items-center gap-1", mismatched && "text-warning")}>
      {mismatched ? <TriangleAlert className="size-3" /> : null}
      {action.target_name}
      {action.target_public !== true ? ` · ${t(($) => $.quick_actions.target_private)}` : ""}
    </span>
  );
}

/** 新建/编辑弹窗的表单状态。 */
interface FormState {
  name: string;
  description: string;
  assigneeType: QuickActionAssigneeType;
  assigneeId: string;
  prompt: string;
  visibility: QuickActionVisibility;
}

/** 空表单：默认公开可见、指派给 agent、无目标。 */
const EMPTY_FORM: FormState = {
  name: "",
  description: "",
  assigneeType: "agent",
  assigneeId: "",
  prompt: "",
  visibility: "public",
};

/** 把已存快捷操作映射成表单状态（供编辑回填）。 */
function toFormState(action: QuickAction): FormState {
  return {
    name: action.name,
    description: action.description,
    assigneeType: action.assignee_type === "squad" ? "squad" : "agent",
    assigneeId: action.assignee_id,
    prompt: action.prompt,
    visibility: action.visibility === "private" ? "private" : "public",
  };
}

/**
 * 「快捷操作」设置标签页：目录式列表（搜索 + 新建按钮 + 响应式表格），每行展示名称、
 * 目标（绑定 agent 及可达性）、可见性、使用次数（久未用标黄）、更新时间，⋯ 菜单提供
 * 编辑 / 归档或还原 / 删除。加载时先取含归档的完整列表再本地排序过滤。
 */
export function QuickActionsTab() {
  const { t } = useT("settings");
  const locale = useLocale();
  const wsId = useWorkspaceId();
  const { data: actions = [], isLoading } = useQuery(quickActionListOptions(wsId, true));

  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<QuickAction | null>(null);
  const [creating, setCreating] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<QuickAction | null>(null);

  const createMutation = useCreateQuickAction();
  const updateMutation = useUpdateQuickAction();
  const deleteMutation = useDeleteQuickAction();

  // 按使用频次优先排序；归档的排到后面、未用过的沉底——「闲置」信号放在那里才好使。
  const sorted = useMemo(
    () =>
      [...actions].sort((a, b) => {
        if (a.status !== b.status) return a.status === "active" ? -1 : 1;
        if (b.use_count !== a.use_count) return b.use_count - a.use_count;
        return a.name.localeCompare(b.name);
      }),
    [actions],
  );

  // 名称 + 目标搜索，与 Labels 页的客户端过滤一致：目录最多 30 行，没必要为此
  // 打一次服务端往返。
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return sorted;
    return sorted.filter(
      (a) =>
        a.name.toLowerCase().includes(q) ||
        (a.target_name ?? "").toLowerCase().includes(q),
    );
  }, [sorted, query]);

  const handleArchiveToggle = async (action: QuickAction) => {
    const next = action.status === "active" ? "archived" : "active";
    try {
      await updateMutation.mutateAsync({ id: action.id, status: next });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await deleteMutation.mutateAsync(deleteTarget.id);
      setDeleteTarget(null);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    }
  };

  return (
    <SettingsTab
      title={t(($) => $.quick_actions.title)}
      description={t(($) => $.quick_actions.description)}
    >
      <div className="space-y-5">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="relative w-full sm:max-w-sm">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t(($) => $.quick_actions.search_placeholder)}
              className="pl-9"
            />
          </div>
          <Button className="gap-2" onClick={() => setCreating(true)}>
            <Plus className="size-4" />
            {t(($) => $.quick_actions.add)}
          </Button>
        </div>

        <div className="overflow-hidden rounded-lg border border-surface-border bg-card">
          <div className="hidden grid-cols-[minmax(10rem,1fr)_minmax(9rem,1fr)_6rem_5rem_7rem_2rem] gap-4 border-b border-surface-border bg-muted/20 px-4 py-2.5 text-caption font-medium text-muted-foreground md:grid">
            <span>{t(($) => $.quick_actions.columns.name)}</span>
            <span>{t(($) => $.quick_actions.columns.target)}</span>
            <span>{t(($) => $.quick_actions.columns.visibility)}</span>
            <span>{t(($) => $.quick_actions.columns.usage)}</span>
            <span>{t(($) => $.quick_actions.columns.updated)}</span>
            <span />
          </div>

          {isLoading ? (
            <div className="px-4 py-12 text-center text-body text-muted-foreground">
              {t(($) => $.quick_actions.loading)}
            </div>
          ) : filtered.length === 0 ? (
            <div className="px-4 py-12 text-center">
              <Zap className="mx-auto size-6 text-faint-foreground" />
              <p className="mt-3 text-body font-medium">
                {query
                  ? t(($) => $.quick_actions.no_results)
                  : t(($) => $.quick_actions.empty_title)}
              </p>
              {!query ? (
                <p className="mx-auto mt-1 max-w-sm text-caption text-muted-foreground">
                  {t(($) => $.quick_actions.empty_hint)}
                </p>
              ) : null}
            </div>
          ) : (
            <div className="divide-y divide-surface-border">
              {filtered.map((action) => {
                const stale = isStale(action);
                return (
                  <div
                    key={action.id}
                    className={cn(
                      "grid gap-2 px-4 py-3 md:grid-cols-[minmax(10rem,1fr)_minmax(9rem,1fr)_6rem_5rem_7rem_2rem] md:items-center md:gap-4",
                      action.status !== "active" && "opacity-60",
                    )}
                  >
                    <div className="flex min-w-0 items-center gap-2">
                      <Zap className="size-3.5 shrink-0 text-muted-foreground" />
                      <span className="truncate text-body font-medium">{action.name}</span>
                      {action.status !== "active" ? (
                        <Badge variant="outline" className="shrink-0 font-normal">
                          {t(($) => $.quick_actions.archived)}
                        </Badge>
                      ) : null}
                    </div>
                    <div className="min-w-0 truncate text-caption text-muted-foreground md:text-body">
                      <TargetLine action={action} t={t} />
                    </div>
                    <div className="min-w-0">
                      <VisibilityBadge action={action} t={t} />
                    </div>
                    <span
                      className={cn(
                        "text-caption tabular-nums text-muted-foreground md:text-body",
                        stale && "text-warning",
                      )}
                    >
                      {action.use_count === 0
                        ? t(($) => $.quick_actions.never_used)
                        : t(($) => $.quick_actions.used_count, { count: action.use_count })}
                    </span>
                    <span className="text-caption text-muted-foreground">
                      {new Date(action.updated_at).toLocaleDateString(locale)}
                    </span>
                    <DropdownMenu>
                      <DropdownMenuTrigger
                        render={
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={t(($) => $.quick_actions.actions_open, { name: action.name })}
                          >
                            <MoreHorizontal className="size-4" />
                          </Button>
                        }
                      />
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onClick={() => setEditing(action)}>
                          <Pencil className="size-4" />
                          {t(($) => $.quick_actions.edit)}
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => void handleArchiveToggle(action)}>
                          {action.status === "active" ? (
                            <>
                              <Archive className="size-4" />
                              {t(($) => $.quick_actions.archive)}
                            </>
                          ) : (
                            <>
                              <ArchiveRestore className="size-4" />
                              {t(($) => $.quick_actions.unarchive)}
                            </>
                          )}
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          variant="destructive"
                          onClick={() => setDeleteTarget(action)}
                        >
                          <Trash2 className="size-4" />
                          {t(($) => $.quick_actions.delete)}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </div>

      <QuickActionDialog
        open={creating || editing !== null}
        action={editing}
        onOpenChange={(open) => {
          if (!open) {
            setCreating(false);
            setEditing(null);
          }
        }}
        onSubmit={async (form) => {
          if (editing) {
            await updateMutation.mutateAsync({
              id: editing.id,
              name: form.name,
              description: form.description,
              assignee_type: form.assigneeType,
              assignee_id: form.assigneeId,
              prompt: form.prompt,
              visibility: form.visibility,
            });
          } else {
            await createMutation.mutateAsync({
              name: form.name,
              description: form.description,
              assignee_type: form.assigneeType,
              assignee_id: form.assigneeId,
              prompt: form.prompt,
              visibility: form.visibility,
            });
          }
        }}
      />

      <AlertDialog open={deleteTarget !== null} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.quick_actions.delete_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.quick_actions.delete_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.quick_actions.cancel)}</AlertDialogCancel>
            <AlertDialogAction onClick={handleDelete}>
              {t(($) => $.quick_actions.delete)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

/**
 * 新建 / 编辑快捷操作的弹窗。字段：名称、可见性（公开 / 私有）、目标（AgentPicker）、
 * 提示词；提交前在本地复刻服务端的校验（名称/提示词/目标非空、提示词不得含不支持的
 * 模板变量），让作者边写边看到错误、而不是等提交后才弹错。
 */
function QuickActionDialog({
  open,
  action,
  onOpenChange,
  onSubmit,
}: {
  open: boolean;
  action: QuickAction | null;
  onOpenChange: (open: boolean) => void;
  onSubmit: (form: FormState) => Promise<void>;
}) {
  const { t } = useT("settings");
  const [form, setForm] = useState<FormState>(EMPTY_FORM);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setForm(action ? toFormState(action) : EMPTY_FORM);
  }, [open, action]);

  // 在本地镜像服务端的拒绝规则，让作者边输入边看到、而不是等提交时被打回。
  // 服务端仍是最终裁决方，这里只是个顺手提示。
  const templateToken = useMemo(() => findQuickActionTemplateToken(form.prompt), [form.prompt]);

  const canSave =
    form.name.trim().length > 0 &&
    form.prompt.trim().length > 0 &&
    form.assigneeId.length > 0 &&
    templateToken === null;

  const handleSubmit = async () => {
    if (!canSave) return;
    setSaving(true);
    try {
      await onSubmit(form);
      onOpenChange(false);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {action ? t(($) => $.quick_actions.edit_title) : t(($) => $.quick_actions.create_title)}
          </DialogTitle>
          <DialogDescription>{t(($) => $.quick_actions.dialog_description)}</DialogDescription>
        </DialogHeader>

        {/* space-y-5 与组内 space-y-1.5 的对比是刻意的：字段与字段之间的间隔必须
            明显大于「标签和它自己的控件」之间的间隔，否则四个分组会读成连成一片的文本。 */}
        <div className="space-y-5">
          <div className="space-y-1.5">
            <FieldLabel htmlFor="qa-name">{t(($) => $.quick_actions.field_name)}</FieldLabel>
            <Input
              id="qa-name"
              value={form.name}
              maxLength={32}
              placeholder={t(($) => $.quick_actions.field_name_placeholder)}
              onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
            />
          </div>

          {/* 先问可见性：这是唯一一个会约束其余字段的选择（公开动作只能绑「可公开
              调用」的目标），放前面问能避免填完点保存才被拒。 */}
          <div className="space-y-1.5">
            <FieldLabel>{t(($) => $.quick_actions.field_visibility)}</FieldLabel>
            <div className="grid grid-cols-2 gap-2">
              {(["public", "private"] as const).map((option) => (
                <button
                  key={option}
                  type="button"
                  aria-pressed={form.visibility === option}
                  onClick={() => setForm((f) => ({ ...f, visibility: option }))}
                  className={cn(
                    "flex items-start gap-2 rounded-lg border px-3 py-2 text-left transition-colors",
                    form.visibility === option
                      ? "border-primary bg-accent/50"
                      : "border-surface-border hover:bg-accent/30",
                  )}
                >
                  {option === "public" ? (
                    <Globe className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  ) : (
                    <Lock className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  )}
                  <span className="min-w-0">
                    <span className="block text-body font-medium">
                      {option === "public"
                        ? t(($) => $.quick_actions.visibility_public)
                        : t(($) => $.quick_actions.visibility_private)}
                    </span>
                    <span className="mt-0.5 block text-caption text-muted-foreground">
                      {option === "public"
                        ? t(($) => $.quick_actions.visibility_public_hint)
                        : t(($) => $.quick_actions.visibility_private_hint)}
                    </span>
                  </span>
                </button>
              ))}
            </div>
          </div>

          <div className="space-y-1.5">
            <FieldLabel>{t(($) => $.quick_actions.field_target)}</FieldLabel>
            <AgentPicker
              assignee={form.assigneeId ? { type: form.assigneeType, id: form.assigneeId } : null}
              onChange={(next) =>
                setForm((f) => ({ ...f, assigneeType: next.type, assigneeId: next.id }))
              }
            />
            {/* 「公开」意味着「人人都能跑」，服务端会拒绝绑定一个团队无法调用的目标。
                提前说清楚，把 400 变成用户点保存之前就该建立的预期。 */}
            {form.visibility === "public" ? (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.quick_actions.public_target_hint)}
              </p>
            ) : null}
          </div>

          <div className="space-y-1.5">
            <FieldLabel htmlFor="qa-prompt">{t(($) => $.quick_actions.field_prompt)}</FieldLabel>
            <Textarea
              id="qa-prompt"
              value={form.prompt}
              rows={5}
              placeholder={t(($) => $.quick_actions.field_prompt_placeholder)}
              onChange={(e) => setForm((f) => ({ ...f, prompt: e.target.value }))}
            />
            {templateToken !== null ? (
              <p className="text-caption text-destructive">
                {t(($) => $.quick_actions.template_not_supported, { token: templateToken })}
              </p>
            ) : (
              <p className="inline-flex items-center gap-1 text-caption text-muted-foreground">
                <Info className="size-3" />
                {t(($) => $.quick_actions.prompt_hint)}
              </p>
            )}
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t(($) => $.quick_actions.cancel)}
          </Button>
          <Button onClick={handleSubmit} disabled={!canSave || saving}>
            {t(($) => $.quick_actions.save)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
