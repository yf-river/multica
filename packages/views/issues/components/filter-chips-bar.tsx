"use client";

// 本文件：FilterChipsBar / FilterChipList —— "已激活筛选"chips 条。
// 每个激活的筛选维度渲染成一个小 chip（维度图标 + 取值摘要 + 移除按钮），
// 点击 chip 右上角可单独清除该维度；右侧"清除"一键重置所有筛选（含日期）。
// 已打开保存视图时，chips 只展示用户叠加在视图条件之上的增补，
// 移除某个维度则回到视图自身该维度的值。
// FilterChipList 是无外壳变体，供表单场景（如保存视图对话框）作为
// "添加筛选"入口的列表使用。名称/头像查找复用筛选下拉已有的缓存。

import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { useStatusLabel } from "../utils/status-label";
import { NO_PROPERTY_VALUE } from "../utils/filter";
import { useMemo, type ReactNode } from "react";
import {
  CalendarDays,
  CircleDot,
  FolderKanban,
  SignalHigh,
  Tag,
  User,
  UserPen,
  X,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions, agentListOptions, squadListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import { labelListOptions } from "@multica/core/labels/queries";
import { propertyListOptions } from "@multica/core/properties";
import { isActorPropertyType, isScalarPropertyType, parseActorRef, propertyFilterValueKey, PROPERTY_FILTER_OP_SYMBOLS, type PropertyFilterValue } from "@multica/core/types";
import {
  type ActorFilterValue,
  type FilterDimension,
  type FilterSnapshot,
  type IssueDateFilter,
} from "@multica/core/issues/stores/view-store";
import {
  actorFilterKey,
  type IssueViewBaseline,
} from "@multica/core/issue-views/baseline";
import { useViewStore, useViewStoreApi } from "@multica/core/issues/stores/view-store-context";
import { StatusIcon } from "./status-icon";
import { PriorityIcon } from "./priority-icon";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";

/** 一个被渲染出来的 chip：某个维度连同其已选值的摘要。 */
interface FilterChip {
  key: string;
  icon: ReactNode;
  /** 维度名称，如"状态"。 */
  label: string;
  /** 取值摘要，如"进行中"或"4 个状态"。 */
  value: string;
  /** 摘要文字前至多三枚相互叠压的值图标。 */
  valueIcons?: ReactNode;
  onRemove: () => void;
}

function summarize(names: (string | undefined)[]): string {
  const first = names.find((n): n is string => !!n);
  if (first === undefined) return String(names.length);
  const rest = names.length - 1;
  return rest > 0 ? `${first} +${rest}` : first;
}

const CHIP_ICON_CLASS = "size-3 shrink-0 text-muted-foreground";

/** 叠压的值图标，沿用运行时列表的堆叠样式：每个图标坐在不透明的底上，
 *  下面那个不会透出来。 */
function IconStack({ children }: { children: ReactNode[] }) {
  return (
    <span className="flex items-center -space-x-1">
      {children.map((child, i) => (
        <span
          key={i}
          className="inline-flex size-4 items-center justify-center rounded-full bg-background ring-1 ring-border"
        >
          {child}
        </span>
      ))}
    </span>
  );
}

/**
 * chip 值的人名查找器，按键方式与筛选实际存储 actor 的方式一致。
 *
 * 成员用 `user_id` 作 key，而不是成员行自身的 id：每个 actor 筛选值——
 * 负责人、创建人、以及成员类属性——携带的都是用户 id。按 `Member.id` 做 key
 * 会静默地查不到任何东西（MUL-6286 评审结论）。
 *
 * 解析不到的名字返回 undefined，让调用方直接省略该值，而不是渲染
 * "Unknown" 之类的占位。
 */
export function buildChipActorNames(
  members: readonly { user_id: string; name: string }[],
  agents: readonly { id: string; name: string }[],
  squads: readonly { id: string; name: string }[],
): (actor: ActorFilterValue) => string | undefined {
  const byKey = new Map<string, string>();
  for (const m of members) byKey.set(`member:${m.user_id}`, m.name);
  for (const a of agents) byKey.set(`agent:${a.id}`, a.name);
  for (const s of squads) byKey.set(`squad:${s.id}`, s.name);
  return (actor: ActorFilterValue) => byKey.get(`${actor.type}:${actor.id}`);
}

/**
 * 把成员类属性的筛选值（"member:<uuid>"）转成 avatar 头像堆栈用的 actor 引用。
 * 解析不了的值直接丢弃——chip 退化成更少的头像，而不是渲染一个坏头像。
 */
export function actorFilterValues(selected: PropertyFilterValue[]): ActorFilterValue[] {
  return selected
    .map((value) => parseActorRef(value))
    .filter((ref): ref is NonNullable<ReturnType<typeof parseActorRef>> => ref !== null)
    .map((ref) => ({ type: ref.kind, id: ref.id }));
}

/**
 * 是否有任何活动的属性筛选指向成员（actor）类型定义。
 *
 * chips 条按需惰性加载各目录，而成员属性是唯一一种"chip 想显示有意义内容
 * 就必须加载成员目录"的属性类型：它的值是引用而非配置选项，缺了目录，
 * 该 chip 只能渲染一个干巴巴的数字（MUL-6286 评审结论）。
 */
export function hasActorPropertyFilterSelection(
  propertyFilters: Record<string, PropertyFilterValue[]>,
  properties: { id: string; type: string }[],
): boolean {
  return Object.entries(propertyFilters).some(
    ([propertyId, selected]) =>
      selected.length > 0 &&
      isActorPropertyType(properties.find((p) => p.id === propertyId)?.type ?? ""),
  );
}

/** 头像版堆叠——头像本身不透明，只需背景色的分隔环即可。 */
function AvatarStack({ actors }: { actors: ActorFilterValue[] }) {
  return (
    <span className="flex items-center -space-x-1">
      {actors.slice(0, 3).map((actor) => (
        <span
          key={`${actor.type}:${actor.id}`}
          className="inline-flex rounded-full ring-2 ring-background"
        >
          <ActorAvatar
            actorType={actor.type}
            actorId={actor.id}
            size="xs"
            profileLink={false}
          />
        </span>
      ))}
    </span>
  );
}

/** 标签和单选项属性用的彩色圆点堆叠。 */
function DotStack({ colors }: { colors: string[] }) {
  if (colors.length === 0) return null;
  return (
    <IconStack>
      {colors.slice(0, 3).map((color, i) => (
        <span
          key={i}
          className="size-2.5 rounded-full"
          style={{ backgroundColor: color }}
        />
      ))}
    </IconStack>
  );
}

/**
 * 针对外围 ViewStoreProvider 的 store 汇总"已激活筛选"chips——
 * 页面里作用于真实 surface store，保存视图对话框里作用于草稿 store。
 * 名称查找复用筛选下拉已填充的缓存；缓存未命中时该 chip 退化为纯计数，
 * 绝不让查询失败卡住渲染。
 */
function useFilterChips(
  dateFilter: IssueDateFilter | null,
  onDateFilterChange?: (filter: IssueDateFilter | null) => void,
  baseline?: IssueViewBaseline,
) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const resolveStatusLabel = useStatusLabel(wsId);
  const { categoryOf, colorOf } = useIssueStatuses(wsId);

  const statusFilters = useViewStore((s) => s.statusFilters);
  const priorityFilters = useViewStore((s) => s.priorityFilters);
  const assigneeFilters = useViewStore((s) => s.assigneeFilters);
  const includeNoAssignee = useViewStore((s) => s.includeNoAssignee);
  const creatorFilters = useViewStore((s) => s.creatorFilters);
  const projectFilters = useViewStore((s) => s.projectFilters);
  const includeNoProject = useViewStore((s) => s.includeNoProject);
  const labelFilters = useViewStore((s) => s.labelFilters);
  const propertyFilters = useViewStore((s) => s.propertyFilters);
  const store = useViewStoreApi();

  const hasStoreFilters =
    statusFilters.length > 0 ||
    priorityFilters.length > 0 ||
    assigneeFilters.length > 0 ||
    includeNoAssignee ||
    creatorFilters.length > 0 ||
    projectFilters.length > 0 ||
    includeNoProject ||
    labelFilters.length > 0 ||
    Object.values(propertyFilters).some((selected) => selected.length > 0);
  const showDateChip = !!onDateFilterChange && !!dateFilter;

  const enabled = hasStoreFilters;
  const { data: workspaceProperties = [] } = useQuery({
    ...propertyListOptions(wsId),
    enabled: enabled && Object.keys(propertyFilters).length > 0,
  });
  // 活动的成员属性筛选还需要成员目录，否则它的 chip 只能显示干巴巴的计数、
  // 看不出筛的是谁。目录查询先返回，因此下面这个开关在其第二次渲染时才翻 true。
  const hasActorPropertyFilter = useMemo(
    () => hasActorPropertyFilterSelection(propertyFilters, workspaceProperties),
    [propertyFilters, workspaceProperties],
  );
  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled:
      enabled &&
      (assigneeFilters.length > 0 || creatorFilters.length > 0 || hasActorPropertyFilter),
  });
  const { data: agents = [] } = useQuery({
    ...agentListOptions(wsId),
    enabled: enabled && (assigneeFilters.length > 0 || creatorFilters.length > 0),
  });
  const { data: squads = [] } = useQuery({
    ...squadListOptions(wsId),
    enabled: enabled && assigneeFilters.some((f) => f.type === "squad"),
  });
  const { data: projects = [] } = useQuery({
    ...projectListOptions(wsId),
    enabled: enabled && projectFilters.length > 0,
  });
  const { data: labels = [] } = useQuery({
    ...labelListOptions(wsId),
    enabled: enabled && labelFilters.length > 0,
  });
  const actorName = useMemo(
    () => buildChipActorNames(members, agents, squads),
    [members, agents, squads],
  );

  // 已保存视图里的 chips 只显示用户叠加在视图之上的增补；移除某个维度会回到
  // 该维度在视图里的取值（绝不会是空）。没有基线时就是普通的整维清除。
  const clearDimension = (dimension: FilterDimension) => {
    if (!baseline) {
      store.getState().clearFilterDimension(dimension);
      return;
    }
    const s = store.getState();
    const raw = baseline.raw;
    const current: FilterSnapshot = {
      statusFilters: s.statusFilters,
      priorityFilters: s.priorityFilters,
      assigneeFilters: s.assigneeFilters,
      includeNoAssignee: s.includeNoAssignee,
      creatorFilters: s.creatorFilters,
      projectFilters: s.projectFilters,
      includeNoProject: s.includeNoProject,
      labelFilters: s.labelFilters,
      propertyFilters: s.propertyFilters,
    };
    switch (dimension) {
      case "status":
        s.resetFiltersTo({ ...current, statusFilters: raw.statusFilters });
        break;
      case "priority":
        s.resetFiltersTo({ ...current, priorityFilters: raw.priorityFilters });
        break;
      case "assignee":
        s.resetFiltersTo({
          ...current,
          assigneeFilters: raw.assigneeFilters,
          includeNoAssignee: raw.includeNoAssignee,
        });
        break;
      case "creator":
        s.resetFiltersTo({ ...current, creatorFilters: raw.creatorFilters });
        break;
      case "project":
        s.resetFiltersTo({
          ...current,
          projectFilters: raw.projectFilters,
          includeNoProject: raw.includeNoProject,
        });
        break;
      case "label":
        s.resetFiltersTo({ ...current, labelFilters: raw.labelFilters });
        break;
      default: {
        const propertyId = dimension.slice("property:".length);
        const propertyFilters = { ...current.propertyFilters };
        const base = raw.propertyFilters[propertyId];
        if (base) propertyFilters[propertyId] = base;
        else delete propertyFilters[propertyId];
        s.resetFiltersTo({ ...current, propertyFilters });
      }
    }
  };

  // 视图已经固定的值在这里不可见——把它们从 chips 中减去。
  const deltaStatus = baseline
    ? statusFilters.filter((s) => !baseline.status.has(s))
    : statusFilters;
  const deltaPriority = baseline
    ? priorityFilters.filter((p) => !baseline.priority.has(p))
    : priorityFilters;
  const deltaAssignees = baseline
    ? assigneeFilters.filter((a) => !baseline.assignee.has(actorFilterKey(a)))
    : assigneeFilters;
  const deltaNoAssignee = baseline
    ? includeNoAssignee && !baseline.includeNoAssignee
    : includeNoAssignee;
  const deltaCreators = baseline
    ? creatorFilters.filter((a) => !baseline.creator.has(actorFilterKey(a)))
    : creatorFilters;
  const deltaProjects = baseline
    ? projectFilters.filter((id) => !baseline.project.has(id))
    : projectFilters;
  const deltaNoProject = baseline
    ? includeNoProject && !baseline.includeNoProject
    : includeNoProject;
  const deltaLabels = baseline
    ? labelFilters.filter((id) => !baseline.label.has(id))
    : labelFilters;
  const deltaProperties: Record<string, PropertyFilterValue[]> = {};
  for (const [id, selected] of Object.entries(propertyFilters)) {
    const fixed = baseline?.property.get(id);
    const delta = fixed
      ? selected.filter((v) => !fixed.has(propertyFilterValueKey(v)))
      : selected;
    if (delta.length > 0) deltaProperties[id] = delta;
  }

  const chips: FilterChip[] = [];

  const [onlyStatus] = deltaStatus;
  if (deltaStatus.length > 0) {
    chips.push({
      key: "status",
      icon: <CircleDot className={CHIP_ICON_CLASS} />,
      label: t(($) => $.filters.section_status),
      valueIcons: (
        <IconStack>
          {deltaStatus.slice(0, 3).map((s) => (
            <StatusIcon
              key={s}
              status={s}
              category={categoryOf(s)}
              color={colorOf(s)}
              className="size-3"
            />
          ))}
        </IconStack>
      ),
      value:
        onlyStatus !== undefined && deltaStatus.length === 1
          ? resolveStatusLabel(onlyStatus)
          : t(($) => $.filters.chip_status_count, { count: deltaStatus.length }),
      onRemove: () => clearDimension("status"),
    });
  }
  const [onlyPriority] = deltaPriority;
  if (deltaPriority.length > 0) {
    chips.push({
      key: "priority",
      icon: <SignalHigh className={CHIP_ICON_CLASS} />,
      label: t(($) => $.filters.section_priority),
      valueIcons: (
        <IconStack>
          {deltaPriority.slice(0, 3).map((p) => (
            <PriorityIcon key={p} priority={p} className="size-3" />
          ))}
        </IconStack>
      ),
      value:
        onlyPriority !== undefined && deltaPriority.length === 1
          ? t(($) => $.priority[onlyPriority])
          : t(($) => $.filters.chip_priority_count, { count: deltaPriority.length }),
      onRemove: () => clearDimension("priority"),
    });
  }
  if (deltaAssignees.length > 0 || deltaNoAssignee) {
    const names = deltaAssignees.map(actorName);
    if (deltaNoAssignee) names.push(t(($) => $.filters.no_assignee));
    chips.push({
      key: "assignee",
      icon: <User className={CHIP_ICON_CLASS} />,
      label: t(($) => $.filters.section_assignee),
      valueIcons: deltaAssignees.length > 0 ? <AvatarStack actors={deltaAssignees} /> : undefined,
      value: summarize(names),
      onRemove: () => clearDimension("assignee"),
    });
  }
  if (deltaCreators.length > 0) {
    chips.push({
      key: "creator",
      icon: <UserPen className={CHIP_ICON_CLASS} />,
      label: t(($) => $.filters.section_creator),
      valueIcons: <AvatarStack actors={deltaCreators} />,
      value: summarize(deltaCreators.map(actorName)),
      onRemove: () => clearDimension("creator"),
    });
  }
  if (deltaProjects.length > 0 || deltaNoProject) {
    const projectById = new Map(projects.map((p) => [p.id, p]));
    const names = deltaProjects.map((id) => projectById.get(id)?.title);
    if (deltaNoProject) names.push(t(($) => $.filters.no_project));
    const emojis = deltaProjects
      .map((id) => projectById.get(id)?.icon)
      .filter((icon): icon is string => !!icon);
    chips.push({
      key: "project",
      icon: <FolderKanban className={CHIP_ICON_CLASS} />,
      label: t(($) => $.filters.section_project),
      valueIcons:
        emojis.length > 0 ? (
          <IconStack>
            {emojis.slice(0, 3).map((emoji, i) => (
              <span key={i} className="text-micro leading-none">{emoji}</span>
            ))}
          </IconStack>
        ) : undefined,
      value: summarize(names),
      onRemove: () => clearDimension("project"),
    });
  }
  if (deltaLabels.length > 0) {
    const labelById = new Map(labels.map((l) => [l.id, l]));
    chips.push({
      key: "label",
      icon: <Tag className={CHIP_ICON_CLASS} />,
      label: t(($) => $.filters.section_label),
      valueIcons: (
        <DotStack
          colors={deltaLabels
            .map((id) => labelById.get(id)?.color)
            .filter((c): c is string => !!c)}
        />
      ),
      value: summarize(deltaLabels.map((id) => labelById.get(id)?.name)),
      onRemove: () => clearDimension("label"),
    });
  }
  for (const [propertyId, selected] of Object.entries(deltaProperties)) {
    if (selected.length === 0) continue;
    const definition = workspaceProperties.find((p) => p.id === propertyId);
    // 钉在已归档定义上的过期筛选会在查询前被 surface 控制器剥离；
    // 这里直接隐藏它的 chip，别渲染一个"幽灵"。
    if (!definition) continue;
    // 成员属性没有配置选项——它的值是成员引用，因此名称查成员目录、图标用头像。
    const actorProperty = isActorPropertyType(definition.type);
    const actorValues = actorProperty ? actorFilterValues(selected) : [];
    const optionName = (member: PropertyFilterValue): string | undefined => {
      if (member === NO_PROPERTY_VALUE) {
        return t(($) => $.pickers.custom_property.none);
      }
      // 标量操作符成员用操作符做摘要；比较符号是语言无关的，
      // 而"文字式"操作符（如 contains/before/after）则用专门的 chip 短语。
      if (typeof member === "object") {
        if (member.op === "contains") {
          return t(($) => $.filters.chip_op_contains, { value: member.value });
        }
        if (member.op === "before") {
          return t(($) => $.filters.chip_op_before, { value: member.value });
        }
        if (member.op === "after") {
          return t(($) => $.filters.chip_op_after, { value: member.value });
        }
        const symbol = PROPERTY_FILTER_OP_SYMBOLS[member.op] ?? member.op;
        return `${symbol} ${member.value}`;
      }
      if (actorProperty) {
        const ref = parseActorRef(member);
        return ref ? actorName({ type: ref.kind, id: ref.id }) : undefined;
      }
      if (definition.type === "checkbox") {
        return member === "true"
          ? t(($) => $.pickers.custom_property.true_label)
          : t(($) => $.pickers.custom_property.false_label);
      }
      // 标量属性没有选项列表——筛选值本身就是标签。
      if (isScalarPropertyType(definition.type)) {
        return member;
      }
      return definition.config.options?.find((o) => o.id === member)?.name;
    };
    const optionColors =
      definition.type === "checkbox" || actorProperty
        ? []
        : selected
            .map((id) => definition.config.options?.find((o) => o.id === id)?.color)
            .filter((c): c is string => !!c);
    chips.push({
      key: `property:${propertyId}`,
      icon: <Tag className={CHIP_ICON_CLASS} />,
      label: definition.name,
      valueIcons: actorValues.length > 0 ? (
        <AvatarStack actors={actorValues} />
      ) : optionColors.length > 0 ? (
        <DotStack colors={optionColors} />
      ) : undefined,
      value: summarize(selected.map(optionName)),
      onRemove: () => clearDimension(`property:${propertyId}`),
    });
  }
  if (showDateChip && dateFilter) {
    const fieldLabel =
      dateFilter.field === "created_at"
        ? t(($) => $.filters.date_field_created)
        : t(($) => $.filters.date_field_updated);
    const short = (dateOnly: string) => {
      const [, m, d] = dateOnly.split("-");
      return `${Number(m)}/${Number(d)}`;
    };
    chips.push({
      key: "date",
      icon: <CalendarDays className={CHIP_ICON_CLASS} />,
      label: fieldLabel,
      value:
        dateFilter.from === dateFilter.to
          ? short(dateFilter.from)
          : `${short(dateFilter.from)} - ${short(dateFilter.to)}`,
      onRemove: () => onDateFilterChange?.(null),
    });
  }

  const clearAll = () => {
    if (baseline) {
      // 精确回到该视图自身的条件。
      store.getState().resetFiltersTo(baseline.raw);
      onDateFilterChange?.(null);
      return;
    }
    store.getState().clearFilters();
    onDateFilterChange?.(null);
  };

  return { chips, hasAny: chips.length > 0, clearAll };
}

function ChipSpan({ chip }: { chip: FilterChip }) {
  const { t } = useT("issues");
  return (
    // 按维度设 key：某维度首次有值时才让 chip 播放一次进入动画；
    // 之后值的更新只重渲染、不重播动画。不做退场动画——
    // 为这 150ms 的小效果上全套卸载机制不值当。
    <span className="flex h-6 max-w-72 items-center gap-1.5 rounded-md bg-background pl-2 pr-1 text-caption shadow-xs animate-in fade-in-0 zoom-in-95 duration-150">
      <span className="flex shrink-0 items-center gap-1 text-muted-foreground">
        {chip.icon}
        <span>{chip.label}</span>
      </span>
      <span className="flex min-w-0 items-center gap-1">
        {chip.valueIcons}
        <span className="truncate font-medium">{chip.value}</span>
      </span>
      <button
        type="button"
        aria-label={t(($) => $.filters.chip_remove, { name: chip.label })}
        onClick={chip.onRemove}
        className="flex size-4 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-accent hover:text-foreground"
      >
        <X className="size-3" />
      </button>
    </span>
  );
}

/**
 * 供表单场景（保存视图对话框）使用的裸 chips 列表：没有横条外壳与页面边距。
 * `trailing` 渲染在所有 chips 之后——即"添加筛选"触发器槽位。
 * 这里 chips 落在对话框背景上而非暗色横条内，所以给它们加了一圈边框。
 * 添加筛选期间菜单的定位稳定性由菜单自身处理（IssueFilterMenu 的
 * `freezeAnchor`），不归本列表管。
 */
export function FilterChipList({ trailing }: { trailing?: ReactNode }) {
  const { chips } = useFilterChips(null);
  return (
    <div className="flex flex-wrap items-center gap-1.5 [&>span]:border [&>span]:border-border [&>span]:shadow-none">
      {chips.map((chip) => (
        <ChipSpan key={chip.key} chip={chip} />
      ))}
      {trailing}
    </div>
  );
}

/**
 * 位于问题页头部下方的"已激活筛选"横向 chips 条。没有任何筛选激活时
 * 不渲染。每个 chip 只清除它自己的维度；右侧"清除"重置一切（包括日期——
 * 日期筛选保存在视图 store 之外）。
 */
export function FilterChipsBar({
  dateFilter = null,
  onDateFilterChange,
  onSave,
  saveLabel,
  viewBaseline,
}: {
  dateFilter?: IssueDateFilter | null;
  onDateFilterChange?: (filter: IssueDateFilter | null) => void;
  /** 是否展示"保存/编辑"按钮。支持保存视图的宿主传入它来打开对话框；
   *  不支持的宿主省略它。 */
  onSave?: () => void;
  /** 覆盖尾部按钮文案——视图模式下为"编辑"/"另存"。 */
  saveLabel?: string;
  /** 已打开的保存视图：chips 只显示叠加在它之上的增补。 */
  viewBaseline?: IssueViewBaseline;
}) {
  const { t } = useT("issues");
  const { chips, hasAny, clearAll } = useFilterChips(dateFilter, onDateFilterChange, viewBaseline);

  if (!hasAny) return null;

  return (
    <div className="mx-4 mb-2 flex shrink-0 flex-wrap items-center gap-1.5 rounded-lg bg-muted/50 px-2 py-1.5">
      {chips.map((chip) => (
        <ChipSpan key={chip.key} chip={chip} />
      ))}
      <span className="ml-auto flex items-center gap-1.5">
        <Button
          variant="ghost"
          size="sm"
          onClick={clearAll}
          className="h-6 px-2 text-caption text-muted-foreground"
        >
          {t(($) => $.filters.chip_clear)}
        </Button>
        {onSave && (
          <Button
            variant="outline"
            size="sm"
            onClick={onSave}
            className="h-6 px-2 text-caption"
          >
            {saveLabel ?? t(($) => $.filters.chip_save)}
          </Button>
        )}
      </span>
    </div>
  );
}
