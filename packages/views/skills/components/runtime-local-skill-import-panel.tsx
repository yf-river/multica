// 本文件职责：从「本地 runtime」批量导入技能的面板——选择一台本地 runtime（按机器分组）、
// 勾选要导入的技能（支持搜索与单个改名/改描述）、批量导入；命名冲突时可逐个/全部决定
// 覆盖或改名或跳过，导入过程实时显示进度与逐条结果。
"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  Download,
  HardDrive,
  Loader2,
  RefreshCw,
  Search,
  SkipForward,
  XCircle,
} from "lucide-react";
import type {
  RuntimeLocalSkillImportConflict,
  RuntimeLocalSkillSummary,
  Skill,
} from "@multica/core/types";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  runtimeDisplayLabel,
  runtimeListOptions,
  runtimeLocalSkillsKeys,
  runtimeLocalSkillsOptions,
  resolveRuntimeLocalSkillImport,
} from "@multica/core/runtimes";
import {
  memberListOptions,
  skillDetailOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Progress } from "@multica/ui/components/ui/progress";
import { Textarea } from "@multica/ui/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useScrollFade } from "@multica/ui/hooks/use-scroll-fade";
import {
  UI_EASE_OUT,
  UI_MOTION_DURATION,
} from "@multica/ui/lib/motion";
import { useT } from "../../i18n";
import { HighlightText } from "../../search/highlight-text";
import { ProviderLogo } from "../../runtimes/components/provider-logo";
import {
  buildRuntimeMachines,
  runtimeRowLabel,
} from "../../runtimes/components/runtime-machines";
import { isNameConflictError } from "../lib/utils";

// ---------------------------------------------------------------------------
// 类型
// ---------------------------------------------------------------------------

/** 单个技能导入的结果。 */
type BulkImportResult = {
  key: string;
  name: string;
  description?: string;
  status: "created" | "updated" | "conflict" | "skipped" | "failed";
  conflict?: RuntimeLocalSkillImportConflict;
  error?: string;
  skill?: Skill;
};

/** 批量导入的整体状态机。 */
type BulkImportState = {
  phase: "idle" | "importing" | "resolving" | "done" | "cancelled";
  total: number;
  completed: number;
  selectedCount: number;
  results: BulkImportResult[];
};

/** 冲突的处置动作：覆盖 / 改名 / 跳过。 */
type ConflictResolutionAction = "overwrite" | "rename" | "skip";

/** 某个冲突的处置状态：动作 +（改名为）新名字。 */
type ConflictResolutionState = {
  action: ConflictResolutionAction;
  renameName: string;
};

/** 批量导入状态的初始值（空闲态）。 */
const INITIAL_BULK_STATE: BulkImportState = {
  phase: "idle",
  total: 0,
  completed: 0,
  selectedCount: 0,
  results: [],
};

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

/**
 * 最大并发导入数。越大越快，但对 daemon/网络的瞬时压力也越大。
 *
 * 超时不变量：IMPORT_CONCURRENCY × 心跳周期（约 15s）必须落在
 * runtimeLocalSkillPendingTimeout（server/internal/handler/runtime_local_skills.go）
 * 与 IMPORT_POLL_TIMEOUT_MS（packages/core/runtimes/local-skills.ts）之内。
 * 另见 server/internal/handler/daemon.go 的 maxLocalSkillImportBatch。
 */
const IMPORT_CONCURRENCY = 10;

/** 冲突时「改名」的默认新名字。 */
function defaultRenameName(name: string): string {
  return `${name} copy`;
}

/** 按结果状态挑一个小图标（绿对勾=新建、蓝刷新=更新、黄三角=冲突等）。 */
function ResultIcon({ status }: { status: BulkImportResult["status"] }) {
  switch (status) {
    case "created":
      return <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-green-600" />;
    case "updated":
      return <RefreshCw className="h-3.5 w-3.5 shrink-0 text-blue-600" />;
    case "conflict":
      return <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-amber-600" />;
    case "skipped":
      return <SkipForward className="h-3.5 w-3.5 shrink-0 text-yellow-600" />;
    case "failed":
      return <AlertCircle className="h-3.5 w-3.5 shrink-0 text-destructive" />;
  }
}

// ---------------------------------------------------------------------------
// 带勾选框的技能行
// ---------------------------------------------------------------------------

/**
 * 单个技能的勾选行：名字/provider/描述/路径展示（命中的搜索词高亮），点击整行切换勾选；
 * 选中且为「唯一选中」时下方展开行内编辑区（可改导入用名与描述）。
 */
function SkillItem({
  skill,
  checked,
  onToggle,
  disabled,
  expanded,
  editName,
  editDescription,
  onNameChange,
  onDescriptionChange,
  query = "",
}: {
  skill: RuntimeLocalSkillSummary;
  checked: boolean;
  onToggle: () => void;
  disabled?: boolean;
  expanded?: boolean;
  editName?: string;
  editDescription?: string;
  onNameChange?: (v: string) => void;
  onDescriptionChange?: (v: string) => void;
  /** 当前搜索词；命中片段会在本行里高亮。 */
  query?: string;
}) {
  const { t } = useT("skills");
  return (
    <div
      className={`overflow-hidden rounded-lg border transition-colors ${
        checked ? "border-primary bg-primary/5" : "hover:bg-accent/40"
      } ${disabled ? "pointer-events-none opacity-60" : ""}`}
    >
      <div
        role="button"
        tabIndex={disabled ? -1 : 0}
        onClick={onToggle}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            onToggle();
          }
        }}
        className="flex w-full items-start gap-3 px-4 py-3 text-left"
      >
        <Checkbox
          checked={checked}
          tabIndex={-1}
          className="pointer-events-none mt-0.5"
        />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-body font-medium">
              <HighlightText text={skill.name} query={query} />
            </span>
            <Badge variant="secondary">
              <HighlightText text={skill.provider} query={query} />
            </Badge>
          </div>
          {skill.description && (
            // 搜索时去掉两行截断：命中可能落在前两行之外，若仍截断会被剪掉看不见。
            <p
              className={`mt-1 text-caption text-muted-foreground ${
                query.trim() ? "" : "line-clamp-2"
              }`}
            >
              <HighlightText text={skill.description} query={query} />
            </p>
          )}
          <p className="mt-1 truncate font-mono text-caption text-muted-foreground">
            <HighlightText text={skill.source_path} query={query} />
          </p>
        </div>
        <Badge variant="outline" className="shrink-0">
          {t(($) => $.runtime_import.skill_files, { count: skill.file_count })}
        </Badge>
      </div>

      {expanded && (
        <div className="space-y-2.5 border-t bg-card px-4 py-3">
          <div className="space-y-1">
            <Label className="text-caption text-muted-foreground">
              {t(($) => $.runtime_import.skill_name_label)}
            </Label>
            <Input
              value={editName ?? ""}
              onChange={(e) => onNameChange?.(e.target.value)}
              placeholder={skill.name}
              className="h-8 text-body"
            />
          </div>
          <div className="space-y-1">
            <Label className="text-caption text-muted-foreground">
              {t(($) => $.runtime_import.skill_description_label)}
            </Label>
            <Textarea
              value={editDescription ?? ""}
              onChange={(e) => onDescriptionChange?.(e.target.value)}
              placeholder={t(($) => $.runtime_import.skill_description_placeholder)}
              rows={2}
              className="resize-none text-body"
            />
          </div>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// 汇总视图（批量导入完成后展示）
// ---------------------------------------------------------------------------

/** 批量导入完成/取消后的汇总：新建/更新/冲突/跳过/失败 5 类计数 + 逐条结果列表。 */
function BulkImportSummary({ results }: { results: BulkImportResult[] }) {
  const { t } = useT("skills");
  const created = results.filter((r) => r.status === "created");
  const updated = results.filter((r) => r.status === "updated");
  const conflicts = results.filter((r) => r.status === "conflict");
  const skipped = results.filter((r) => r.status === "skipped");
  const failed = results.filter((r) => r.status === "failed");

  return (
    <div className="space-y-4 py-2">
      {/* 各类计数 */}
      <div className="grid grid-cols-2 gap-2 text-center sm:grid-cols-5">
        <div className="rounded-md bg-green-50 px-3 py-2 dark:bg-green-950/30">
          <div className="text-title font-semibold text-green-700 dark:text-green-400">
            {created.length}
          </div>
          <div className="text-caption text-muted-foreground">
            {t(($) => $.runtime_import.bulk_summary_created)}
          </div>
        </div>
        <div className="rounded-md bg-blue-50 px-3 py-2 dark:bg-blue-950/30">
          <div className="text-title font-semibold text-blue-700 dark:text-blue-400">
            {updated.length}
          </div>
          <div className="text-caption text-muted-foreground">
            {t(($) => $.runtime_import.bulk_summary_updated)}
          </div>
        </div>
        <div className="rounded-md bg-amber-50 px-3 py-2 dark:bg-amber-950/30">
          <div className="text-title font-semibold text-amber-700 dark:text-amber-400">
            {conflicts.length}
          </div>
          <div className="text-caption text-muted-foreground">
            {t(($) => $.runtime_import.bulk_summary_conflicts)}
          </div>
        </div>
        <div className="rounded-md bg-yellow-50 px-3 py-2 dark:bg-yellow-950/30">
          <div className="text-title font-semibold text-yellow-700 dark:text-yellow-400">
            {skipped.length}
          </div>
          <div className="text-caption text-muted-foreground">
            {t(($) => $.runtime_import.bulk_summary_skipped)}
          </div>
        </div>
        <div className="rounded-md bg-red-50 px-3 py-2 dark:bg-red-950/30">
          <div className="text-title font-semibold text-red-700 dark:text-red-400">
            {failed.length}
          </div>
          <div className="text-caption text-muted-foreground">
            {t(($) => $.runtime_import.bulk_summary_failed)}
          </div>
        </div>
      </div>

      {/* 逐条结果明细 */}
      <div className="max-h-64 space-y-1 overflow-y-auto rounded-md border p-2">
        {results.map((r) => (
          <div
            key={r.key}
            className="flex items-center gap-2 rounded px-2 py-1.5 text-caption"
          >
            <ResultIcon status={r.status} />
            <span className="min-w-0 flex-1 truncate">{r.name}</span>
            {r.error && (
              <span className="max-w-[200px] shrink-0 truncate text-muted-foreground">
                {r.error}
              </span>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

/**
 * 冲突解决面板——列出每个命名冲突（新技能名字撞上已存在技能），逐条选择
 * 覆盖/改名/跳过；批量时还提供「全部覆盖」「全部跳过」快捷按钮。
 */
function ConflictResolutionPanel({
  conflicts,
  resolutions,
  onChange,
  onResolveNow,
  onOverwriteAll,
  onSkipAll,
}: {
  conflicts: BulkImportResult[];
  resolutions: Record<string, ConflictResolutionState>;
  onChange: (key: string, next: ConflictResolutionState) => void;
  onResolveNow?: (key: string, next: ConflictResolutionState) => void;
  onOverwriteAll: () => void;
  onSkipAll: () => void;
}) {
  const { t } = useT("skills");
  const wsId = useWorkspaceId();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const single = conflicts.length === 1;
  const canOverwriteAny = conflicts.some((r) => r.conflict?.can_overwrite);

  return (
    <div className="space-y-4 py-2">
      <div className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-body text-amber-950 dark:border-amber-900/60 dark:bg-amber-950/30 dark:text-amber-100">
        <div className="flex items-start gap-2">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
          <div className="min-w-0">
            <p className="font-medium">
              {single
                ? t(($) => $.runtime_import.conflict_single_title)
                : t(($) => $.runtime_import.conflict_bulk_title, {
                    count: conflicts.length,
                  })}
            </p>
            <p className="mt-1 text-caption">
              {t(($) => $.runtime_import.conflict_hint)}
            </p>
          </div>
        </div>
      </div>

      {!single && (
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={onOverwriteAll}
            disabled={!canOverwriteAny}
          >
            <RefreshCw className="h-3 w-3" />
            {t(($) => $.runtime_import.conflict_overwrite_all)}
          </Button>
          <Button type="button" size="sm" variant="outline" onClick={onSkipAll}>
            <SkipForward className="h-3 w-3" />
            {t(($) => $.runtime_import.conflict_skip_all)}
          </Button>
        </div>
      )}

      <div className="space-y-2">
        {conflicts.map((r) => {
          const resolution =
            resolutions[r.key] ??
            ({
              action: r.conflict?.can_overwrite ? "overwrite" : "rename",
              renameName: defaultRenameName(r.name),
            } satisfies ConflictResolutionState);
          const creatorId = r.conflict?.existing_created_by;
          const creatorName = creatorId
            ? members.find((m) => m.user_id === creatorId)?.name
            : undefined;
          return (
            <div key={r.key} className="rounded-lg border bg-card p-3">
              <div className="flex items-start gap-2">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-body font-medium">{r.name}</div>
                  {r.error && (
                    <p className="mt-1 text-caption text-destructive">{r.error}</p>
                  )}
                  {!r.conflict?.can_overwrite && (
                    <p className="mt-1 text-caption text-muted-foreground">
                      {creatorName
                        ? t(($) => $.runtime_import.conflict_locked_creator, {
                            creator: creatorName,
                          })
                        : t(($) => $.runtime_import.conflict_locked)}
                    </p>
                  )}
                </div>
              </div>

              <div className="mt-3 flex flex-wrap gap-2">
                <Button
                  type="button"
                  size="sm"
                  variant={
                    resolution.action === "overwrite" ? "default" : "outline"
                  }
                  onClick={() => {
                    const next = {
                      action: "overwrite",
                      renameName: resolution.renameName,
                    } satisfies ConflictResolutionState;
                    if (single && r.conflict?.can_overwrite && onResolveNow) {
                      onResolveNow(r.key, next);
                    } else {
                      onChange(r.key, next);
                    }
                  }}
                  disabled={!r.conflict?.can_overwrite}
                >
                  <RefreshCw className="h-3 w-3" />
                  {t(($) => $.runtime_import.conflict_overwrite)}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant={resolution.action === "rename" ? "default" : "outline"}
                  onClick={() =>
                    onChange(r.key, {
                      action: "rename",
                      renameName: resolution.renameName,
                    })
                  }
                >
                  {t(($) => $.runtime_import.conflict_rename)}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant={resolution.action === "skip" ? "default" : "outline"}
                  onClick={() =>
                    onChange(r.key, {
                      action: "skip",
                      renameName: resolution.renameName,
                    })
                  }
                >
                  <XCircle className="h-3 w-3" />
                  {single
                    ? t(($) => $.runtime_import.conflict_cancel)
                    : t(($) => $.runtime_import.conflict_skip)}
                </Button>
              </div>

              {resolution.action === "rename" && (
                <div className="mt-3 space-y-1">
                  <Label className="text-caption text-muted-foreground">
                    {t(($) => $.runtime_import.conflict_rename_label)}
                  </Label>
                  <Input
                    value={resolution.renameName}
                    onChange={(e) =>
                      onChange(r.key, {
                        action: "rename",
                        renameName: e.target.value,
                      })
                    }
                    className="h-8 text-body"
                  />
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 面板主体
// ---------------------------------------------------------------------------

/**
 * 「从本地 runtime 导入技能」面板。
 *
 * 顶部选一台本地 runtime（按机器分组）并展示其在线状态；中部是技能勾选列表
 * （搜索、全选/半选、单选时行内改名/改描述）；底部按钮随阶段变化——空闲时「导入」、
 * 导入中显示进度条与取消、冲突时进入逐条处置、完成/取消后展示汇总与「完成」。
 * 单一导入成功后回调 onImported 用于跳详情，批量导入结束后回调 onBulkDone。
 */
export function RuntimeLocalSkillImportPanel({
  onImported,
  onBulkDone,
}: {
  onImported?: (skill: Skill) => void;
  onBulkDone?: () => void;
}) {
  const { t } = useT("skills");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const shouldReduceMotion = useReducedMotion() ?? false;

  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const localRuntimes = useMemo(
    () =>
      runtimes.filter(
        (r) =>
          r.runtime_mode === "local" &&
          (userId == null || r.owner_id === userId),
      ),
    [runtimes, userId],
  );

  // 把本地 runtime 按「机器」分组，让选择器读起来像「机器 → provider/runtime」
  // （别名友好），而不是一长串裸 daemon 名（MUL-5248）。与 agent runtime 选择器
  // 复用同一份数据源。
  const runtimeMachines = useMemo(
    () =>
      buildRuntimeMachines(localRuntimes, {
        now: Date.now(),
      }),
    [localRuntimes, userId],
  );

  const [selectedRuntimeId, setSelectedRuntimeId] = useState<string>("");
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(new Set());
  const [bulkState, setBulkState] = useState<BulkImportState>(INITIAL_BULK_STATE);
  const [conflictResolutions, setConflictResolutions] = useState<
    Record<string, ConflictResolutionState>
  >({});
  const [skillSearchQuery, setSkillSearchQuery] = useState("");
  const cancelRef = useRef(false);
  // 单选时的行内编辑字段（仅当恰好勾选了 1 个技能时显示）
  const [editName, setEditName] = useState("");
  const [editDescription, setEditDescription] = useState("");

  const importing = bulkState.phase === "importing";
  const resolvingConflicts = bulkState.phase === "resolving";
  const busy = importing || resolvingConflicts;

  useEffect(() => {
    setSelectedRuntimeId((prev) => prev || localRuntimes[0]?.id || "");
  }, [localRuntimes]);

  useEffect(() => {
    setSelectedKeys(new Set());
    setBulkState(INITIAL_BULK_STATE);
    setConflictResolutions({});
    setSkillSearchQuery("");
    setEditName("");
    setEditDescription("");
  }, [selectedRuntimeId]);

  const selectedRuntime = localRuntimes.find((r) => r.id === selectedRuntimeId);
  const canBrowseSkills =
    !!selectedRuntimeId && selectedRuntime?.status === "online";
  const skillsQuery = useQuery({
    ...runtimeLocalSkillsOptions(selectedRuntimeId || null),
    enabled: canBrowseSkills,
  });
  const runtimeSkills = useMemo(
    () => skillsQuery.data?.skills ?? [],
    [skillsQuery.data],
  );
  const filteredRuntimeSkills = useMemo(() => {
    const query = skillSearchQuery.trim().toLowerCase();
    if (!query) return runtimeSkills;

    // 只搜行内实际渲染的字段（name/provider/description/path），这样每次命中都能
    // 在结果里高亮出来；刻意排除 `skill.key`——它不展示，若只命中 key，用户会看到
    // 一条毫无来由的结果。
    return runtimeSkills.filter((skill) =>
      [skill.name, skill.description, skill.provider, skill.source_path].some(
        (value) => value?.toLowerCase().includes(query) ?? false,
      ),
    );
  }, [runtimeSkills, skillSearchQuery]);

  // 行内编辑针对的对象：只有恰好勾选 1 个技能时才有意义，否则为 undefined。
  const singleSelectedSkill =
    selectedKeys.size === 1
      ? runtimeSkills.find((s) => selectedKeys.has(s.key))
      : undefined;

  // -- 勾选辅助 --

  const toggleSkill = (key: string) => {
    setSelectedKeys((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      // 恰好收窄到单选时，把该技能的名字/描述预填进编辑框，供用户改后再导入。
      if (next.size === 1) {
        const only = runtimeSkills.find((s) => next.has(s.key));
        if (only) {
          setEditName(only.name);
          setEditDescription(only.description ?? "");
        }
      }
      return next;
    });
  };

  const toggleAll = () => {
    const visibleKeys = new Set(filteredRuntimeSkills.map((s) => s.key));
    const visibleSelected =
      visibleKeys.size > 0 &&
      filteredRuntimeSkills.every((s) => selectedKeys.has(s.key));

    setSelectedKeys((prev) => {
      const next = new Set(prev);
      if (visibleSelected) {
        for (const key of visibleKeys) next.delete(key);
      } else {
        for (const key of visibleKeys) next.add(key);
      }
      if (next.size === 1) {
        const only = runtimeSkills.find((s) => next.has(s.key));
        if (only) {
          setEditName(only.name);
          setEditDescription(only.description ?? "");
        }
      }
      return next;
    });
  };

  const allSelected =
    filteredRuntimeSkills.length > 0 &&
    filteredRuntimeSkills.every((s) => selectedKeys.has(s.key));
  const someSelected =
    filteredRuntimeSkills.some((s) => selectedKeys.has(s.key)) && !allSelected;
  const pendingConflicts = bulkState.results.filter(
    (r) => r.status === "conflict" && r.conflict,
  );
  const canApplyConflictResolutions =
    pendingConflicts.length > 0 &&
    pendingConflicts.every((r) => {
      const resolution = conflictResolutions[r.key];
      if (!resolution) return false;
      if (resolution.action === "overwrite") return !!r.conflict?.can_overwrite;
      if (resolution.action === "rename") return !!resolution.renameName.trim();
      return true;
    });

  const setConflictResolution = (
    key: string,
    next: ConflictResolutionState,
  ) => {
    setConflictResolutions((prev) => ({ ...prev, [key]: next }));
  };

  const seedConflictResolutions = (results: BulkImportResult[]) => {
    const next: Record<string, ConflictResolutionState> = {};
    for (const r of results) {
      if (r.status !== "conflict" || !r.conflict) continue;
      next[r.key] = {
        action: r.conflict.can_overwrite ? "overwrite" : "rename",
        renameName: defaultRenameName(r.name),
      };
    }
    setConflictResolutions(next);
  };

  const refreshImportedSkills = async (results: BulkImportResult[]) => {
    await Promise.all([
      qc.invalidateQueries({
        queryKey: runtimeLocalSkillsKeys.forRuntime(selectedRuntimeId),
      }),
      qc.invalidateQueries({ queryKey: workspaceKeys.skills(wsId) }),
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) }),
    ]);

    for (const r of results) {
      if ((r.status === "created" || r.status === "updated") && r.skill) {
        qc.setQueryData(
          skillDetailOptions(wsId, r.skill.id).queryKey,
          r.skill,
        );
      }
    }
  };

  // -- 批量导入主流程 --

  const handleBulkImport = async () => {
    if (!selectedRuntimeId || selectedKeys.size === 0) return;

    const skillsToImport = runtimeSkills.filter((s) => selectedKeys.has(s.key));
    const total = skillsToImport.length;

    cancelRef.current = false;
    setBulkState({
      phase: "importing",
      total,
      completed: 0,
      selectedCount: total,
      results: [],
    });

    const results: BulkImportResult[] = [];

    const importOne = async (skill: RuntimeLocalSkillSummary) => {
      // 单选时用用户在行内编辑过的名字/描述；多选时保持原名原描述。
      const importName =
        total === 1 ? editName.trim() || skill.name : skill.name;
      const importDescription =
        total === 1
          ? editDescription.trim() || skill.description || undefined
          : skill.description || undefined;
      try {
        const result = await resolveRuntimeLocalSkillImport(selectedRuntimeId, {
          skill_key: skill.key,
          name: importName,
          description: importDescription,
          supports_conflict: true,
        });
        if (result.status === "conflict") {
          results.push({
            key: skill.key,
            name: importName,
            description: importDescription,
            status: "conflict",
            conflict: result.conflict,
          });
        } else {
          results.push({
            key: skill.key,
            name: result.skill?.name ?? importName,
            description: importDescription,
            status: result.status,
            skill: result.skill,
          });
        }
      } catch (error) {
        const msg = error instanceof Error ? error.message : "";
        const isSkipped = isNameConflictError(msg);
        results.push({
          key: skill.key,
          name: skill.name,
          description: importDescription,
          status: isSkipped ? "skipped" : "failed",
          error: msg || t(($) => $.runtime_import.toast_import_failed),
        });
      }

      setBulkState((prev) => ({
        ...prev,
        completed: prev.completed + 1,
        results: [...results],
      }));
    };

    // 并发池：同时最多 IMPORT_CONCURRENCY 条在途；满员就等最先完成的那条让出名额。
    const executing = new Set<Promise<void>>();
    for (const skill of skillsToImport) {
      if (cancelRef.current) break;
      const p = importOne(skill).then(() => {
        executing.delete(p);
      });
      executing.add(p);
      if (executing.size >= IMPORT_CONCURRENCY) {
        await Promise.race(executing);
      }
    }
    await Promise.all(executing);

    await refreshImportedSkills(results);

    const conflicts = results.filter((r) => r.status === "conflict");
    if (!cancelRef.current && conflicts.length > 0) {
      seedConflictResolutions(results);
      setBulkState((prev) => ({
        ...prev,
        phase: "resolving",
      }));
      return;
    }

    setBulkState((prev) => ({
      ...prev,
      phase: cancelRef.current ? "cancelled" : "done",
    }));
  };

  const handleApplyConflictResolutions = async (
    resolutionOverrides: Record<string, ConflictResolutionState> = {},
  ) => {
    if (!selectedRuntimeId || pendingConflicts.length === 0) return;

    const conflicts = [...pendingConflicts];
    let nextResults = bulkState.results;
    const applyResult = (key: string, next: Partial<BulkImportResult>) => {
      nextResults = nextResults.map((r) =>
        r.key === key ? { ...r, ...next } : r,
      );
      setBulkState((prev) => ({
        ...prev,
        results: prev.results.map((r) =>
          r.key === key ? { ...r, ...next } : r,
        ),
      }));
    };

    setBulkState((prev) => ({
      ...prev,
      phase: "importing",
      total: conflicts.length,
      completed: 0,
    }));

    for (const r of conflicts) {
      const resolution =
        resolutionOverrides[r.key] ?? conflictResolutions[r.key];
      if (!resolution) {
        applyResult(r.key, {
          status: "failed",
          error: t(($) => $.runtime_import.conflict_missing_resolution),
        });
        setBulkState((prev) => ({ ...prev, completed: prev.completed + 1 }));
        continue;
      }

      if (resolution.action === "skip") {
        applyResult(r.key, { status: "skipped", error: undefined });
        setBulkState((prev) => ({ ...prev, completed: prev.completed + 1 }));
        continue;
      }

      try {
        const result = await resolveRuntimeLocalSkillImport(selectedRuntimeId, {
          skill_key: r.key,
          name:
            resolution.action === "rename"
              ? resolution.renameName.trim()
              : r.name,
          description: r.description,
          supports_conflict: true,
          ...(resolution.action === "overwrite" && r.conflict
            ? {
                action: "overwrite" as const,
                target_skill_id: r.conflict.existing_skill_id,
              }
            : {}),
        });

        if (result.status === "conflict") {
          applyResult(r.key, {
            name:
              resolution.action === "rename"
                ? resolution.renameName.trim()
                : r.name,
            status: "conflict",
            conflict: result.conflict,
            error: t(($) => $.runtime_import.conflict_name_still_exists),
          });
          if (result.conflict) {
            setConflictResolution(r.key, {
              action: result.conflict.can_overwrite ? "overwrite" : "rename",
              renameName: defaultRenameName(
                resolution.action === "rename"
                  ? resolution.renameName.trim()
                  : r.name,
              ),
            });
          }
        } else {
          applyResult(r.key, {
            name: result.skill?.name ?? r.name,
            status: result.status,
            skill: result.skill,
            conflict: undefined,
            error: undefined,
          });
        }
      } catch (error) {
        const msg = error instanceof Error ? error.message : "";
        applyResult(r.key, {
          status: "failed",
          error: msg || t(($) => $.runtime_import.toast_import_failed),
        });
      }

      setBulkState((prev) => ({ ...prev, completed: prev.completed + 1 }));
    }

    await refreshImportedSkills(nextResults);
    const unresolved = nextResults.some((r) => r.status === "conflict");
    setBulkState((prev) => ({
      ...prev,
      results: nextResults,
      phase: unresolved ? "resolving" : "done",
    }));
  };

  const canImport =
    !!selectedRuntime &&
    selectedRuntime.status === "online" &&
    selectedKeys.size > 0 &&
    // 单选时要求名字非空——用户可能正在行内改名，空名提交会被后端拒绝。
    (selectedKeys.size > 1 || !!editName.trim()) &&
    !busy;

  const handleCancel = () => {
    cancelRef.current = true;
  };

  const scrollRef = useRef<HTMLDivElement>(null);
  const fadeStyle = useScrollFade(scrollRef);

  // -- 中部内容区：按阶段切换（勾选列表 / 进度 / 冲突处置 / 汇总） --

  const middle = (() => {
    // 导入中：进度条 + 实时逐条结果。
    if (bulkState.phase === "importing") {
      const pct =
        bulkState.total > 0
          ? Math.round((bulkState.completed / bulkState.total) * 100)
          : 0;
      return (
        <div className="space-y-4 py-4">
          <div className="text-center">
            <Loader2 className="mx-auto h-6 w-6 animate-spin text-primary" />
            <p className="mt-3 text-body font-medium">
              {t(($) => $.runtime_import.bulk_progress, {
                completed: bulkState.completed,
                total: bulkState.total,
              })}
            </p>
          </div>
          <Progress value={pct} />
          {/* 实时滚动的逐条导入结果 */}
          <div className="max-h-48 space-y-1 overflow-y-auto">
            {bulkState.results.map((r) => (
              <div
                key={r.key}
                className="flex items-center gap-2 rounded px-2 py-1 text-caption"
              >
                <ResultIcon status={r.status} />
                <span className="truncate">{r.name}</span>
              </div>
            ))}
          </div>
        </div>
      );
    }

    // 导入结束（完成或用户取消）后展示汇总视图。
    if (bulkState.phase === "done" || bulkState.phase === "cancelled") {
      return <BulkImportSummary results={bulkState.results} />;
    }

    if (bulkState.phase === "resolving") {
      return (
        <ConflictResolutionPanel
          conflicts={pendingConflicts}
          resolutions={conflictResolutions}
          onChange={setConflictResolution}
          onResolveNow={(key, next) => {
            setConflictResolution(key, next);
            void handleApplyConflictResolutions({ [key]: next });
          }}
          onOverwriteAll={() => {
            setConflictResolutions((prev) => {
              const next = { ...prev };
              for (const r of pendingConflicts) {
                if (!r.conflict?.can_overwrite) continue;
                next[r.key] = {
                  action: "overwrite",
                  renameName: prev[r.key]?.renameName ?? defaultRenameName(r.name),
                };
              }
              return next;
            });
          }}
          onSkipAll={() => {
            setConflictResolutions((prev) => {
              const next = { ...prev };
              for (const r of pendingConflicts) {
                next[r.key] = {
                  action: "skip",
                  renameName: prev[r.key]?.renameName ?? defaultRenameName(r.name),
                };
              }
              return next;
            });
          }}
        />
      );
    }

    // --- 空闲阶段：技能勾选列表（含无 runtime/离线/加载失败等前置态） ---

    if (localRuntimes.length === 0) {
      return (
        <div className="rounded-lg border border-dashed px-4 py-10 text-center">
          <p className="text-body text-muted-foreground">
            {t(($) => $.runtime_import.no_local_runtimes_title)}
          </p>
          <p className="mt-1 text-caption text-muted-foreground">
            {t(($) => $.runtime_import.no_local_runtimes_hint)}
          </p>
        </div>
      );
    }
    if (!selectedRuntime) {
      return (
        <div className="rounded-lg border border-dashed px-4 py-10 text-center">
          <p className="text-body text-muted-foreground">
            {t(($) => $.runtime_import.choose_runtime)}
          </p>
        </div>
      );
    }
    if (selectedRuntime.status !== "online") {
      return (
        <div className="flex items-start gap-2 rounded-md bg-warning/10 px-3 py-2 text-caption text-muted-foreground">
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warning" />
          {t(($) => $.runtime_import.must_be_online)}
        </div>
      );
    }
    if (skillsQuery.isLoading) {
      return (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <div key={i} className="rounded-lg border px-4 py-3">
              <Skeleton className="h-4 w-32" />
              <Skeleton className="mt-2 h-3 w-48" />
            </div>
          ))}
        </div>
      );
    }
    if (skillsQuery.error) {
      return (
        <div className="flex items-start gap-2 rounded-md bg-destructive/10 px-3 py-2 text-caption text-destructive">
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          {skillsQuery.error instanceof Error
            ? skillsQuery.error.message
            : t(($) => $.runtime_import.load_failed)}
        </div>
      );
    }
    if (!skillsQuery.data?.supported) {
      return (
        <div className="flex items-start gap-2 rounded-md bg-muted/50 px-3 py-2 text-caption text-muted-foreground">
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          {t(($) => $.runtime_import.not_supported)}
        </div>
      );
    }
    if (runtimeSkills.length === 0) {
      return (
        <div className="rounded-lg border border-dashed px-4 py-10 text-center">
          <p className="text-body text-muted-foreground">
            {t(($) => $.runtime_import.no_skills_title)}
          </p>
          <p className="mt-1 text-caption text-muted-foreground">
            {t(($) => $.runtime_import.no_skills_hint)}
          </p>
        </div>
      );
    }
    return (
      <div className="flex flex-col gap-2">
        <div className="relative">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={skillSearchQuery}
            onChange={(e) => setSkillSearchQuery(e.target.value)}
            placeholder={t(($) => $.runtime_import.search_placeholder)}
            className="h-9 pl-8 text-body"
          />
        </div>

        {filteredRuntimeSkills.length === 0 ? (
          <div className="rounded-lg border border-dashed px-4 py-8 text-center">
            <p className="text-body text-muted-foreground">
              {t(($) => $.runtime_import.no_search_results_title)}
            </p>
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.runtime_import.no_search_results_hint, {
                query: skillSearchQuery.trim(),
              })}
            </p>
          </div>
        ) : (
          <>
            {/* 全选表头：点此勾选/取消当前搜索结果的全部技能（半选态由 indeterminate 表达） */}
            <label className="flex cursor-pointer items-center gap-2 px-1 py-1">
              <input
                type="checkbox"
                checked={allSelected}
                ref={(el) => {
                  if (el) el.indeterminate = someSelected;
                }}
                onChange={toggleAll}
                className="cursor-pointer accent-primary"
              />
              <span className="text-caption text-muted-foreground">
                {t(($) => $.runtime_import.select_all, {
                  count: filteredRuntimeSkills.length,
                })}
              </span>
            </label>

            {filteredRuntimeSkills.map((s) => (
              <SkillItem
                key={s.key}
                skill={s}
                checked={selectedKeys.has(s.key)}
                onToggle={() => toggleSkill(s.key)}
                disabled={importing}
                expanded={singleSelectedSkill?.key === s.key}
                editName={
                  singleSelectedSkill?.key === s.key ? editName : undefined
                }
                editDescription={
                  singleSelectedSkill?.key === s.key
                    ? editDescription
                    : undefined
                }
                onNameChange={setEditName}
                onDescriptionChange={setEditDescription}
                query={skillSearchQuery}
              />
            ))}
          </>
        )}
      </div>
    );
  })();

  // -- 「完成」按钮：导入收尾后决定去详情页还是关闭弹窗 --

  const handleDone = () => {
    const succeeded = bulkState.results.filter(
      (r) => r.status === "created" || r.status === "updated",
    );
    // 单选流程：跳转到导入成功的那个技能详情页。
    // 多选流程：直接关闭弹窗——即使只有一条成功也一样。
    if (
      bulkState.selectedCount === 1 &&
      succeeded.length === 1 &&
      succeeded[0]!.skill
    ) {
      onImported?.(succeeded[0]!.skill);
    } else {
      onBulkDone?.();
    }
  };

  const footerContent =
    bulkState.phase === "done" || bulkState.phase === "cancelled" ? (
      <>
        <div className="min-w-0 flex-1 text-caption text-muted-foreground">
          {bulkState.phase === "cancelled"
            ? t(($) => $.runtime_import.bulk_cancelled_hint)
            : t(($) => $.runtime_import.bulk_complete_hint)}
        </div>
        <Button type="button" size="sm" onClick={handleDone}>
          {t(($) => $.runtime_import.bulk_done_button)}
        </Button>
      </>
    ) : resolvingConflicts ? (
      <>
        <div className="min-w-0 flex-1 text-caption text-muted-foreground">
          {t(($) => $.runtime_import.conflict_footer, {
            count: pendingConflicts.length,
          })}
        </div>
        <Button
          type="button"
          size="sm"
          onClick={() => void handleApplyConflictResolutions()}
          disabled={!canApplyConflictResolutions}
        >
          {t(($) => $.runtime_import.conflict_apply_button)}
        </Button>
      </>
    ) : importing ? (
      <>
        <div className="min-w-0 flex-1 text-caption text-muted-foreground">
          {t(($) => $.runtime_import.bulk_progress, {
            completed: bulkState.completed,
            total: bulkState.total,
          })}
        </div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={handleCancel}
        >
          {t(($) => $.runtime_import.bulk_cancel_button)}
        </Button>
      </>
    ) : (
      <>
        <div className="min-w-0 flex-1 text-caption text-muted-foreground">
          {singleSelectedSkill ? (
            <>
              {t(($) => $.runtime_import.ready)}{" "}
              <span className="font-medium text-foreground">
                {editName.trim() || singleSelectedSkill.name}
              </span>{" "}
              {t(($) => $.runtime_import.into_workspace)}
            </>
          ) : selectedKeys.size > 1 ? (
            t(($) => $.runtime_import.bulk_ready, {
              count: selectedKeys.size,
            })
          ) : (
            t(($) => $.runtime_import.select_skill)
          )}
        </div>
        <Button
          type="button"
          size="sm"
          onClick={handleBulkImport}
          disabled={!canImport}
        >
          <Download className="h-3 w-3" />
          {selectedKeys.size > 1
            ? t(($) => $.runtime_import.bulk_import_button, {
                count: selectedKeys.size,
              })
            : t(($) => $.runtime_import.import_button)}
        </Button>
      </>
    );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* 吸顶区：runtime 选择器 + 当前选中项的在线状态 */}
      <div
        aria-disabled={busy || undefined}
        className={`shrink-0 space-y-2 border-b px-5 py-3 ${
          busy ? "pointer-events-none opacity-60" : ""
        }`}
      >
        <div className="space-y-1.5">
          <label className="text-caption text-muted-foreground">
            {t(($) => $.runtime_import.runtime_label)}
          </label>
          <Select
            items={localRuntimes.map((runtime) => ({
              value: runtime.id,
              label: runtimeDisplayLabel(runtime),
            }))}
            value={selectedRuntimeId}
            onValueChange={(v) => v && setSelectedRuntimeId(v)}
          >
            <SelectTrigger className="w-full">
              <SelectValue placeholder={t(($) => $.runtime_import.runtime_placeholder)}>
                {selectedRuntime ? (
                  <>
                    <ProviderLogo
                      provider={selectedRuntime.provider}
                      className="h-4 w-4 shrink-0"
                    />
                    <span className="truncate">
                      {runtimeDisplayLabel(selectedRuntime)}
                    </span>
                  </>
                ) : null}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {runtimeMachines.map((machine) => (
                <SelectGroup key={machine.id}>
                  <SelectLabel>{machine.title}</SelectLabel>
                  {machine.runtimes.map((r) => (
                    <SelectItem key={r.id} value={r.id}>
                      <ProviderLogo
                        provider={r.provider}
                        className="h-4 w-4 shrink-0"
                      />
                      <span className="truncate">
                        {runtimeRowLabel(r, machine.title)}
                      </span>
                    </SelectItem>
                  ))}
                </SelectGroup>
              ))}
            </SelectContent>
          </Select>
        </div>

        {selectedRuntime && (
          <div className="flex items-center gap-2 rounded-md border bg-muted/20 px-3 py-1.5 text-caption text-muted-foreground">
            <HardDrive className="h-3.5 w-3.5 shrink-0" />
            <span className="min-w-0 flex-1 truncate">
              {runtimeDisplayLabel(selectedRuntime)}
            </span>
            <Badge
              variant={
                selectedRuntime.status === "online" ? "secondary" : "outline"
              }
            >
              {selectedRuntime.status}
            </Badge>
          </div>
        )}
      </div>

      {/* 可滚动的中部内容区（带首尾渐隐滚动效果） */}
      <div
        ref={scrollRef}
        style={fadeStyle}
        aria-disabled={importing || undefined}
        className={`min-h-0 flex-1 overflow-y-auto px-5 py-3 ${
          importing && bulkState.phase !== "importing" ? "pointer-events-none opacity-60" : ""
        }`}
      >
        <AnimatePresence mode="wait" initial={false}>
          <motion.div
            key={bulkState.phase}
            initial={{
              opacity: 0,
              transform: shouldReduceMotion
                ? "translateY(0)"
                : "translateY(8px)",
            }}
            animate={{
              opacity: 1,
              transform: "translateY(0)",
              transition: {
                duration: shouldReduceMotion
                  ? UI_MOTION_DURATION.fast
                  : UI_MOTION_DURATION.standard,
                ease: UI_EASE_OUT,
              },
            }}
            exit={{
              opacity: 0,
              transform: shouldReduceMotion
                ? "translateY(0)"
                : "translateY(-8px)",
              transition: {
                duration: shouldReduceMotion
                  ? UI_MOTION_DURATION.fast
                  : UI_MOTION_DURATION.micro,
                ease: UI_EASE_OUT,
              },
            }}
          >
            {middle}
            {bulkState.phase === "idle" && (
              <p className="mt-3 text-caption text-muted-foreground">
                {t(($) => $.runtime_import.ignored_files_hint)}
              </p>
            )}
          </motion.div>
        </AnimatePresence>
      </div>

      {/* 吸底区：随阶段变化的一排操作按钮（导入 / 取消 / 处置冲突 / 完成） */}
      <div className="flex shrink-0 items-center gap-3 border-t bg-muted/30 px-5 py-3">
        <AnimatePresence mode="wait" initial={false}>
          <motion.div
            key={bulkState.phase}
            className="flex min-w-0 flex-1 items-center gap-3"
            initial={{ opacity: 0 }}
            animate={{
              opacity: 1,
              transition: {
                duration: UI_MOTION_DURATION.fast,
                ease: UI_EASE_OUT,
              },
            }}
            exit={{
              opacity: 0,
              transition: {
                duration: UI_MOTION_DURATION.micro,
                ease: UI_EASE_OUT,
              },
            }}
          >
            {footerContent}
          </motion.div>
        </AnimatePresence>
      </div>
    </div>
  );
}
