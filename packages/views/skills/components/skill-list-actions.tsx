// 本文件职责：技能列表上的「操作」相关 UI——行内 ⋯ 菜单、悬浮批量工具栏，以及它们
// 背后共享的弹窗（添加到 agent、删除、从来源批量更新）。所有权限与可操作集合的判断
// 都基于上下文 SkillActionsContext。
"use client";

import { useState } from "react";
import {
  Check,
  ChevronRight,
  ExternalLink,
  Loader2,
  MoreHorizontal,
  Plus,
  RotateCw,
  Search,
  Trash2,
  X,
} from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent, SkillSummary } from "@multica/core/types";
import { api } from "@multica/core/api";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@multica/ui/components/ui/collapsible";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { useIntentNavigate } from "../../navigation";
import { isRefreshableOrigin, readOrigin } from "../lib/origin";
import { RefreshSkillDialog } from "./refresh-skill-dialog";
import type { SkillRow } from "./skills-page";

/**
 * 行内 ⋯ 菜单与批量工具栏共用的共享上下文，由页面层一次性组装好后透传下来，
 * 内含发起各类操作所需的全部信息（工作区、agent 列表、当前用户、是否 admin）。
 */
export interface SkillActionsContext {
  wsId: string;
  agents: Agent[];
  currentUserId: string | null;
  /** 工作区 owner/admin——可以管理所有 agent，而不只是自己创建的。 */
  isAdmin: boolean;
}

// ---------------------------------------------------------------------------
// 「添加到 agent」弹窗（多选；行内菜单与批量工具栏共用）
// ---------------------------------------------------------------------------

// 「挂载 skill」的权限挂在目标 agent 上（它的 owner，或工作区 owner/admin——
// 见 server/internal/handler/agent.go canManageAgent）。因此普通成员只能看到自己的
// agent；admin 额外能看到其他人的。已经拥有全部所选技能的 agent 显示为勾选且禁用；
// 部分重叠仍可勾选，因为该接口是「确保这些技能」的增量语义且幂等。
function partitionAgents(ctx: SkillActionsContext) {
  const active = ctx.agents.filter((a) => !a.archived_at);
  const mine = active.filter(
    (a) => a.owner_id !== null && a.owner_id === ctx.currentUserId,
  );
  const others = ctx.isAdmin
    ? active.filter(
        (a) => a.owner_id === null || a.owner_id !== ctx.currentUserId,
      )
    : [];
  return { mine, others };
}

/** 单个 agent 的选择行：统计它已拥有的技能数，已全部拥有则禁用并打勾。 */
function AgentPickerRow({
  agent,
  skillIds,
  selected,
  onToggle,
}: {
  agent: Agent;
  skillIds: string[];
  selected: boolean;
  onToggle: (agent: Agent) => void;
}) {
  const { t } = useT("skills");
  const owned = skillIds.filter((id) =>
    agent.skills.some((s) => s.id === id),
  ).length;
  const hasAll = owned === skillIds.length;
  return (
    <button
      type="button"
      disabled={hasAll}
      onClick={() => onToggle(agent)}
      aria-pressed={selected}
      className={cn(
        "flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left transition-colors",
        hasAll
          ? "opacity-50"
          : selected
            ? "bg-accent"
            : "hover:bg-accent/50",
      )}
    >
      {/* 仅作展示——点击事件由外层包裹的 <button> 处理。 */}
      <Checkbox
        checked={hasAll || selected}
        tabIndex={-1}
        className="pointer-events-none"
      />
      <ActorAvatar
        name={agent.name}
        initials={agent.name.slice(0, 2).toUpperCase()}
        avatarUrl={resolvePublicFileUrl(agent.avatar_url)}
        isAgent
        size="md"
      />
      <span className="min-w-0 flex-1 truncate text-body">{agent.name}</span>
      {hasAll ? (
        <Check className="size-3.5 shrink-0 text-muted-foreground" />
      ) : owned > 0 ? (
        <span className="shrink-0 text-caption text-muted-foreground">
          {t(($) => $.actions.has_partial, { owned, total: skillIds.length })}
        </span>
      ) : null}
    </button>
  );
}

// 把前几个技能名渲染成小胶囊，让弹窗说清楚「要添加的是什么」；超出部分折叠成「+N」
// 徽标，其余名字放进 tooltip。与数量无关，单条与批量共用同一布局。
const MAX_SKILL_CHIPS = 3;
const MAX_TOOLTIP_NAMES = 10;

/** 技能名胶囊行：前 N 个直接展示，多余折叠成带 tooltip 的「+N」。 */
function SkillChips({ skills }: { skills: SkillSummary[] }) {
  const visible = skills.slice(0, MAX_SKILL_CHIPS);
  const overflow = skills.slice(MAX_SKILL_CHIPS);
  const chipClass =
    "max-w-[10rem] truncate rounded bg-muted px-1.5 py-0.5 text-caption text-muted-foreground";
  return (
    <div className="flex flex-wrap items-center gap-1">
      {visible.map((s) => (
        <span key={s.id} className={chipClass}>
          {s.name}
        </span>
      ))}
      {overflow.length > 0 && (
        <Tooltip>
          <TooltipTrigger
            render={
              <span className={cn(chipClass, "font-medium")}>
                +{overflow.length}
              </span>
            }
          />
          <TooltipContent side="bottom" className="max-w-64">
            {overflow
              .slice(0, MAX_TOOLTIP_NAMES)
              .map((s) => s.name)
              .join(", ")}
            {overflow.length > MAX_TOOLTIP_NAMES ? "…" : ""}
          </TooltipContent>
        </Tooltip>
      )}
    </div>
  );
}

// 可折叠的 agent 分组；「我的 agent」默认展开，别人的 agent 默认收起。
function AgentGroup({
  label,
  agents,
  defaultOpen,
  skillIds,
  selectedIds,
  onToggle,
}: {
  label: string;
  agents: Agent[];
  defaultOpen: boolean;
  skillIds: string[];
  selectedIds: ReadonlySet<string>;
  onToggle: (agent: Agent) => void;
}) {
  if (agents.length === 0) return null;
  return (
    <Collapsible defaultOpen={defaultOpen}>
      <CollapsibleTrigger className="group/trigger flex w-full items-center gap-1 rounded-md px-2.5 py-1.5 text-caption font-medium text-muted-foreground transition-colors hover:bg-accent/50">
        <ChevronRight className="size-3 stroke-[2.5] transition-transform duration-200 group-data-[panel-open]/trigger:rotate-90" />
        <span>{label}</span>
        <span className="text-muted-foreground">{agents.length}</span>
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-0.5">
        {agents.map((agent) => (
          <AgentPickerRow
            key={agent.id}
            agent={agent}
            skillIds={skillIds}
            selected={selectedIds.has(agent.id)}
            onToggle={onToggle}
          />
        ))}
      </CollapsibleContent>
    </Collapsible>
  );
}

/**
 * 「把所选技能添加到 agent」弹窗（多选）。
 *
 * agent 按「我的 / 其他」分组、支持名称搜索；每个 agent 展示它已拥有/缺少的情况，
 * 已全部拥有的自动禁用。确认后逐个补齐缺失技能（接口是「确保」语义，可重复执行），
 * 成功后刷新 agent 缓存并 toast。
 */
export function AddToAgentDialog({
  skills,
  ctx,
  open,
  onOpenChange,
}: {
  skills: SkillSummary[];
  ctx: SkillActionsContext;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("skills");
  const qc = useQueryClient();
  const [selectedIds, setSelectedIds] = useState<ReadonlySet<string>>(
    new Set(),
  );
  const [saving, setSaving] = useState(false);
  const [query, setQuery] = useState("");

  const skillIds = skills.map((s) => s.id);
  const { mine, others } = partitionAgents(ctx);
  const count = selectedIds.size;

  // 跨两个分组按 agent 名搜索。过滤逐组进行，以保留「我的 / 其他」的分组结构；一旦
  // 输入了查询，两组都被强制展开（靠下面的重挂载 key 实现），让通常收起状态下的
  // 「其他 agent」里的命中项保持可见。
  const trimmedQuery = query.trim().toLowerCase();
  const searching = trimmedQuery.length > 0;
  const matchesQuery = (a: Agent) =>
    !searching || a.name.toLowerCase().includes(trimmedQuery);
  const filteredMine = mine.filter(matchesQuery);
  const filteredOthers = others.filter(matchesQuery);
  const hasAnyAgent = mine.length + others.length > 0;
  const hasMatch = filteredMine.length + filteredOthers.length > 0;

  const handleOpenChange = (v: boolean) => {
    if (saving) return;
    if (!v) {
      setSelectedIds(new Set());
      setQuery("");
    }
    onOpenChange(v);
  };

  const handleToggle = (agent: Agent) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(agent.id)) next.delete(agent.id);
      else next.add(agent.id);
      return next;
    });
  };

  const handleConfirm = async () => {
    const targets = [...mine, ...others].filter((a) => selectedIds.has(a.id));
    if (targets.length === 0) return;
    setSaving(true);
    try {
      for (const agent of targets) {
        const missing = skillIds.filter(
          (id) => !agent.skills.some((s) => s.id === id),
        );
        if (missing.length > 0) {
          await api.addAgentSkills(agent.id, { skill_ids: missing });
        }
      }
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(ctx.wsId) });
      toast.success(
        targets.length === 1 && targets[0]
          ? t(($) => $.actions.added_toast, { name: targets[0].name })
          : t(($) => $.actions.added_multi_toast, { count: targets.length }),
      );
      setSelectedIds(new Set());
      onOpenChange(false);
    } catch (e) {
      toast.error(
        e instanceof Error && e.message
          ? e.message
          : t(($) => $.actions.add_failed_toast),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      {/* 固定高度弹窗：头部/胶囊/footer 保持原位，只有 agent 列表滚动
          （flex-1 + min-h-0）。比默认确认弹窗高一档——这是干活用的选择器，不是一句
          提问。 */}
      <DialogContent className="flex h-[32rem] max-h-[85svh] max-w-lg flex-col">
        <DialogHeader>
          <DialogTitle className="text-body">
            {t(($) => $.actions.add_to_agent)}
          </DialogTitle>
          <DialogDescription className="text-caption">
            {t(($) => $.actions.add_dialog_description)}
          </DialogDescription>
        </DialogHeader>

        <SkillChips skills={skills} />

        {hasAnyAgent && (
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t(($) => $.actions.search_placeholder)}
              className="h-8 pl-7 text-caption"
            />
          </div>
        )}

        <div className="min-h-0 flex-1 overflow-y-auto rounded-lg border bg-card p-1.5">
          {!hasAnyAgent ? (
            <div className="py-6 text-center text-caption text-muted-foreground">
              {t(($) => $.actions.no_agents)}
            </div>
          ) : !hasMatch ? (
            <div className="py-6 text-center text-caption text-muted-foreground">
              {t(($) => $.actions.no_agents_match)}
            </div>
          ) : (
            <>
              <AgentGroup
                key={`mine-${searching}`}
                label={t(($) => $.actions.my_agents)}
                agents={filteredMine}
                defaultOpen
                skillIds={skillIds}
                selectedIds={selectedIds}
                onToggle={handleToggle}
              />
              <AgentGroup
                key={`others-${searching}`}
                label={t(($) => $.actions.other_agents)}
                agents={filteredOthers}
                defaultOpen={searching}
                skillIds={skillIds}
                selectedIds={selectedIds}
                onToggle={handleToggle}
              />
            </>
          )}
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="ghost"
            onClick={() => handleOpenChange(false)}
            disabled={saving}
          >
            {t(($) => $.actions.cancel)}
          </Button>
          <Button
            type="button"
            onClick={handleConfirm}
            disabled={saving || count === 0}
          >
            {saving ? (
              <>
                <Loader2 className="h-3 w-3 animate-spin" />
                {t(($) => $.actions.adding)}
              </>
            ) : (
              t(($) => $.actions.add_confirm, { num: count })
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// 删除确认（单条与批量共用一个弹窗）
// ---------------------------------------------------------------------------

/**
 * 「删除技能」确认弹窗。删除前逐条调用接口并给出明确警告；单条时额外点名该技能正被
 * 哪些 agent 使用（有使用方时文案更郑重），成功/失败均有 toast。
 */
export function DeleteSkillsDialog({
  rows,
  ctx,
  open,
  onOpenChange,
  onDeleted,
}: {
  rows: SkillRow[];
  ctx: SkillActionsContext;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDeleted?: () => void;
}) {
  const { t } = useT("skills");
  const qc = useQueryClient();
  const [deleting, setDeleting] = useState(false);
  const single = rows.length === 1 ? rows[0] : null;
  const count = rows.length;

  const handleConfirm = async () => {
    setDeleting(true);
    try {
      for (const row of rows) {
        await api.deleteSkill(row.skill.id);
      }
      qc.invalidateQueries({ queryKey: workspaceKeys.skills(ctx.wsId) });
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(ctx.wsId) });
      toast.success(t(($) => $.actions.deleted_toast, { count }));
      onOpenChange(false);
      onDeleted?.();
    } catch (e) {
      toast.error(
        e instanceof Error && e.message
          ? e.message
          : t(($) => $.actions.delete_failed_toast),
      );
    } finally {
      setDeleting(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!deleting) onOpenChange(v);
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {single
              ? t(($) => $.detail.delete_dialog.title)
              : t(($) => $.actions.delete_dialog_title, { count })}
          </DialogTitle>
          <DialogDescription>
            {single
              ? single.agents.length > 0
                ? t(($) => $.detail.delete_dialog.description_with_agents, {
                    name: single.skill.name,
                    count: single.agents.length,
                  })
                : t(($) => $.detail.delete_dialog.description_no_agents, {
                    name: single.skill.name,
                  })
              : t(($) => $.actions.delete_dialog_desc, { count })}
          </DialogDescription>
        </DialogHeader>
        <div className="rounded-md bg-destructive/10 px-3 py-2 text-caption text-destructive">
          {t(($) => $.detail.delete_dialog.warning)}
        </div>
        <DialogFooter>
          <Button
            type="button"
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={deleting}
          >
            {t(($) => $.detail.delete_dialog.cancel)}
          </Button>
          <Button
            type="button"
            variant="destructive"
            onClick={handleConfirm}
            disabled={deleting}
          >
            {deleting ? (
              <>
                <Loader2 className="h-3 w-3 animate-spin" />
                {t(($) => $.detail.delete_dialog.deleting)}
              </>
            ) : (
              <>
                <Trash2 className="h-3 w-3" />
                {t(($) => $.detail.delete_dialog.confirm)}
              </>
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// 从来源批量更新（批量工具栏）
// ---------------------------------------------------------------------------

/**
 * 「从来源批量更新」的确认并执行弹窗。每个可更新的选中项都会重新下载——刻意不先做
 * 「上游是否新鲜」的探测（产品决策 #6930）：在明确的「更新」点击下，把一个本已最新
 * 的技能重拉一次的成本，好过去逐个探测每个来源来确认它们是最新的。
 *
 * 刷新顺序执行且「逐条容错」（区别于批量删除的全有或全无循环）：某条更新上游改名导致
 * 409 时，不能让整个批次卡住。部分失败会保留当前选择（`onUpdated` 只在干净跑完时触发），
 * 这样重试只需再点一次。
 */
export function UpdateSkillsDialog({
  rows,
  skippedCount,
  ctx,
  open,
  onOpenChange,
  onUpdated,
}: {
  /** 选择集中「可更新」的子集（有编辑权限 + 来源可刷新）。 */
  rows: SkillRow[];
  /** 选择集中「不可更新」的条目数（手动、runtime、无权限）。 */
  skippedCount: number;
  ctx: SkillActionsContext;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 只在整批全部成功后触发——部分失败时保留选择，便于一键重试。 */
  onUpdated?: () => void;
}) {
  const { t } = useT("skills");
  const qc = useQueryClient();
  const [updating, setUpdating] = useState(false);
  const [progress, setProgress] = useState(0);

  const handleConfirm = async () => {
    setUpdating(true);
    let updated = 0;
    let failed = 0;
    try {
      for (const row of rows) {
        setProgress(updated + failed + 1);
        try {
          await api.refreshSkill(row.skill.id);
          updated++;
        } catch {
          failed++;
        }
      }
      qc.invalidateQueries({ queryKey: workspaceKeys.skills(ctx.wsId) });
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(ctx.wsId) });
      if (failed === 0) {
        toast.success(t(($) => $.actions.updated_toast, { count: updated }));
        onOpenChange(false);
        onUpdated?.();
      } else {
        toast.error(
          t(($) => $.actions.update_partial_toast, { count: updated, failed }),
        );
        onOpenChange(false);
      }
    } finally {
      setUpdating(false);
      setProgress(0);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!updating) onOpenChange(v);
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.actions.update_dialog_title)}</DialogTitle>
        </DialogHeader>
        <ul className="space-y-1 text-caption text-muted-foreground">
          <li>
            {t(($) => $.actions.update_summary_selected, {
              count: rows.length,
            })}
          </li>
          {skippedCount > 0 && (
            <li>
              {t(($) => $.actions.update_summary_skipped, {
                count: skippedCount,
              })}
            </li>
          )}
        </ul>
        <div className="rounded-md bg-warning/10 px-3 py-2 text-caption text-muted-foreground">
          {t(($) => $.detail.refresh.dialog.warning)}
        </div>
        <DialogFooter>
          <Button
            type="button"
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={updating}
          >
            {t(($) => $.actions.cancel)}
          </Button>
          <Button
            type="button"
            onClick={handleConfirm}
            disabled={updating || rows.length === 0}
          >
            {updating ? (
              <>
                <Loader2 className="h-3 w-3 animate-spin" />
                {t(($) => $.actions.update_updating, {
                  done: progress,
                  total: rows.length,
                })}
              </>
            ) : (
              <>
                <RotateCw className="h-3 w-3" />
                {t(($) => $.actions.update_confirm, { count: rows.length })}
              </>
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// 行内 ⋯ 菜单
// ---------------------------------------------------------------------------

// 行是普通的 `<div>`，整行导航是鼠标 `onClick`（见 useRowLink），并没有祖先 `<a>`。
// 这里用一层 span 阻止事件冒泡，让打开菜单不会顺带触发行导航——只需 stopPropagation，
// 不需要 preventDefault（没有原生锚点要取消）。
/** 单行技能的 ⋯ 菜单：新标签打开详情 / 添加到 agent /（可刷新时）从来源更新 / 删除。 */
export function SkillRowActions({
  row,
  ctx,
}: {
  row: SkillRow;
  ctx: SkillActionsContext;
}) {
  const { t } = useT("skills");
  const { t: tCommon } = useT("common");
  const paths = useWorkspacePaths();
  const intentNavigate = useIntentNavigate();
  const [addOpen, setAddOpen] = useState(false);
  const [refreshOpen, setRefreshOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);

  const origin = readOrigin(row.skill);
  const canRefresh = row.canEdit && isRefreshableOrigin(origin);

  return (
    <span
      onClick={(e) => e.stopPropagation()}
      className="flex items-center"
    >
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <button
              type="button"
              aria-label={t(($) => $.actions.row_menu)}
              className="flex size-7 items-center justify-center rounded-md text-muted-foreground opacity-0 transition-opacity hover:bg-accent hover:text-accent-foreground group-hover/row:opacity-100 data-popup-open:bg-accent data-popup-open:opacity-100 data-popup-open:text-accent-foreground"
            >
              <MoreHorizontal className="size-4" />
            </button>
          }
        />
        <DropdownMenuContent align="end" className="w-52">
          <DropdownMenuItem
            onClick={() =>
              intentNavigate(
                paths.skillDetail(row.skill.id),
                "foreground-tab",
              )
            }
          >
            <ExternalLink className="size-3.5" />
            {tCommon(($) => $.navigation.open_in_new_tab)}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={() => setAddOpen(true)}>
            <Plus className="size-3.5" />
            {t(($) => $.actions.add_to_agent)}
          </DropdownMenuItem>
          {canRefresh && (
            <DropdownMenuItem onClick={() => setRefreshOpen(true)}>
              <RotateCw className="size-3.5" />
              {t(($) => $.actions.refresh)}
            </DropdownMenuItem>
          )}
          {row.canEdit && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                onClick={() => setDeleteOpen(true)}
              >
                <Trash2 className="size-3.5" />
                {t(($) => $.actions.delete)}
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      <AddToAgentDialog
        skills={[row.skill]}
        ctx={ctx}
        open={addOpen}
        onOpenChange={setAddOpen}
      />
      {canRefresh && (
        <RefreshSkillDialog
          skill={row.skill}
          origin={origin}
          wsId={ctx.wsId}
          open={refreshOpen}
          onOpenChange={setRefreshOpen}
        />
      )}
      <DeleteSkillsDialog
        rows={[row]}
        ctx={ctx}
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
      />
    </span>
  );
}

// ---------------------------------------------------------------------------
// 批量工具栏（悬浮于底部的操作条，与 issues 批量栏同构）
// ---------------------------------------------------------------------------

/**
 * 多选后的悬浮批量工具栏：显示已选数量（可一键清除），提供「添加到 agent」、
 * 「从来源更新」（仅当存在可更新的行）与「删除」（全部行都可删时才可点）。
 */
export function SkillBatchToolbar({
  rows,
  ctx,
  onClear,
}: {
  rows: SkillRow[];
  ctx: SkillActionsContext;
  onClear: () => void;
}) {
  const { t } = useT("skills");
  const [addOpen, setAddOpen] = useState(false);
  const [updateOpen, setUpdateOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);

  if (rows.length === 0) return null;

  const allDeletable = rows.every((r) => r.canEdit);
  const updatable = rows.filter(
    (r) => r.canEdit && isRefreshableOrigin(readOrigin(r.skill)),
  );

  const updateButton = (
    <Button
      variant="ghost"
      size="sm"
      disabled={updatable.length === 0}
      onClick={() => setUpdateOpen(true)}
      className={cn(updatable.length === 0 && "pointer-events-none")}
    >
      <RotateCw className="mr-1 size-3.5" />
      {t(($) => $.actions.update)}
    </Button>
  );

  const deleteButton = (
    <Button
      variant="ghost"
      size="sm"
      disabled={!allDeletable}
      onClick={() => setDeleteOpen(true)}
      className={cn(
        "text-destructive hover:text-destructive",
        !allDeletable && "pointer-events-none",
      )}
    >
      <Trash2 className="mr-1 size-3.5" />
      {t(($) => $.actions.delete)}
    </Button>
  );

  return (
    <>
      {/* 锚定到页面根（relative）而不是视口：开着侧边栏/分栏时，按视口居中会明显偏离
          列表自身的中心。以后每个列表页的批量工具栏都遵循同一规则。 */}
      <div className="absolute bottom-6 left-1/2 z-50 flex -translate-x-1/2 items-center gap-1 rounded-lg border bg-background px-2 py-1.5 shadow-lg max-md:above-chat-launcher">
        <div className="mr-1 flex items-center gap-1.5 border-r pl-1 pr-2">
          <span className="text-body font-medium">
            {t(($) => $.actions.selected, { count: rows.length })}
          </span>
          <button
            type="button"
            aria-label={t(($) => $.actions.clear_selection)}
            onClick={onClear}
            className="rounded p-0.5 transition-colors hover:bg-accent"
          >
            <X className="size-3.5 text-muted-foreground" />
          </button>
        </div>

        <Button variant="ghost" size="sm" onClick={() => setAddOpen(true)}>
          <Plus className="mr-1 size-3.5" />
          {t(($) => $.actions.add_to_agent)}
        </Button>

        {updatable.length > 0 ? (
          updateButton
        ) : (
          <Tooltip>
            <TooltipTrigger
              render={<span className="inline-flex">{updateButton}</span>}
            />
            <TooltipContent side="top">
              {t(($) => $.actions.update_none)}
            </TooltipContent>
          </Tooltip>
        )}

        {allDeletable ? (
          deleteButton
        ) : (
          <Tooltip>
            <TooltipTrigger
              render={<span className="inline-flex">{deleteButton}</span>}
            />
            <TooltipContent side="top">
              {t(($) => $.actions.delete_no_permission)}
            </TooltipContent>
          </Tooltip>
        )}
      </div>

      <AddToAgentDialog
        skills={rows.map((r) => r.skill)}
        ctx={ctx}
        open={addOpen}
        onOpenChange={setAddOpen}
      />
      <UpdateSkillsDialog
        rows={updatable}
        skippedCount={rows.length - updatable.length}
        ctx={ctx}
        open={updateOpen}
        onOpenChange={setUpdateOpen}
        onUpdated={onClear}
      />
      <DeleteSkillsDialog
        rows={rows}
        ctx={ctx}
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        onDeleted={onClear}
      />
    </>
  );
}
