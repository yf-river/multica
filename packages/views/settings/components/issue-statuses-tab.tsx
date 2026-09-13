// 本文件是「问题状态」设置标签页：按类别（todo / in_review 等）分组管理工作区的问题状态，
// 支持新增自定义状态、重命名、改颜色与描述、拖拽排序、归档；内置状态只读展示不可改。

"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Archive,
  GripVertical,
  MoreHorizontal,
  Pencil,
  Plus,
} from "lucide-react";
import { toast } from "sonner";
import {
  DndContext,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import { memberListOptions } from "@multica/core/workspace/queries";
import { issueStatusColor, issueStatusListOptions } from "@multica/core/issue-statuses/queries";
import {
  useArchiveIssueStatus,
  useCreateIssueStatus,
  useReorderIssueStatuses,
  useUpdateIssueStatus,
} from "@multica/core/issue-statuses/mutations";
import { ALL_STATUSES } from "@multica/core/issues/config";
import type { IssueStatusCategory, IssueStatusEntry } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Label as FieldLabel } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { ColorPicker, COLOR_PICKER_PRESETS } from "../../common/color-picker";
import { StatusIcon } from "../../issues/components/status-icon";
import { useStatusLabel } from "../../issues/utils/status-label";
import { useT } from "../../i18n";
import { SettingsTab } from "./settings-layout";

/**
 * 工作区「问题状态」目录管理（MUL-6243）。
 *
 * 页面按「类别」组织，而不是拉成一长串平铺列表，因为类别在这里不是装饰，
 * 而是状态继承到的行为：放在 todo 里的状态会触发指派给它的 agent，
 * 放在 in_review 里的状态会终结一次自动运行。按类别分组，就是为了让这份后果
 * 在管理员挑选类别的那一刻就显现出来——这也是他们唯一能看到后果的时机：
 * 类别创建后不可再改，改了等于悄悄改写每一个挂在该状态上的 issue 的机器语义。
 *
 * 内置状态会展示出来但处于锁定态。每个内置状态就是其类别的"标准定义"，
 * 默认工作区对任何从未打开过本页的用户来说都必须长得一模一样。
 *
 * 界面刻意做得极简（MUL-6422）。类别与它的内置行本是同一概念展示两遍，
 * 行里已有的东西——图标、行为描述——放到头部标题上就是噪音；
 * 所以头部只留类别名和它独有的一件动作。
 */

/** 新建/编辑状态用的表单草稿：名称、描述、类别与颜色。 */
interface StatusDraft {
  name: string;
  description: string;
  category: IssueStatusCategory;
  color: string;
}

// 空白草稿的默认值：新建状态时以此为起点（默认归入 todo、颜色取预设里的第 7 个）。
const EMPTY_DRAFT: StatusDraft = {
  name: "",
  description: "",
  category: "todo",
  color: COLOR_PICKER_PRESETS[6]!,
};

/**
 * 「问题状态」设置标签页：按类别分组展示全部状态，管理员可新建/编辑/归档自定义状态，
 * 并可打开「显示已归档」开关查看归档行。
 */
export function IssueStatusesTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();

  const [showArchived, setShowArchived] = useState(false);
  const [createCategory, setCreateCategory] = useState<IssueStatusCategory | null>(null);
  const [editing, setEditing] = useState<IssueStatusEntry | null>(null);
  const [pendingArchive, setPendingArchive] = useState<IssueStatusEntry | null>(null);

  const { data: statuses = [], isLoading } = useQuery(issueStatusListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentUser = useAuthStore((s) => s.user);
  const myRole = useMemo(() => {
    if (!currentUser) return null;
    return members.find((m) => m.user_id === currentUser.id)?.role ?? null;
  }, [members, currentUser]);
  const isAdmin = myRole === "owner" || myRole === "admin";

  const groups = useMemo(
    () =>
      ALL_STATUSES.map((category) => {
        const inCategory = statuses.filter((s) => s.category === category);
        return {
          category,
          builtIn: inCategory.find((s) => s.is_system),
          // 归档行默认收起来、需要开关才展示，而不是直接丢掉：管理员得能看到
          // 旧 issue 上还残留着的那个状态到底是什么。
          custom: inCategory.filter(
            (s) => !s.is_system && (showArchived || !s.archived_at),
          ),
        };
      }),
    [statuses, showArchived],
  );

  const archivedCount = statuses.filter((s) => !s.is_system && s.archived_at).length;

  return (
    <SettingsTab
      title={t(($) => $.issue_statuses.title)}
      description={t(($) => $.issue_statuses.description)}
    >
      <div className="space-y-4">
        {/* 只有确实存在已归档状态时才显示"显示已归档"开关：一个永远置灰的
            「显示已归档 (0)」按钮是什么也做不了的摆设。 */}
        {archivedCount > 0 && (
          <label className="flex items-center justify-end gap-2 text-caption text-muted-foreground">
            {t(($) => $.issue_statuses.show_archived, { count: archivedCount })}
            <Switch checked={showArchived} onCheckedChange={setShowArchived} />
          </label>
        )}

        {isLoading ? (
          <div className="rounded-lg border border-surface-border bg-card px-4 py-12 text-center text-body text-muted-foreground">
            {t(($) => $.issue_statuses.loading)}
          </div>
        ) : (
          // 用一整块列表而不是七张独立卡片：各个类别只是同一条工作流的几个分段，
          // 若各自套七个边框，读起来会像七个互不相干的设置项。
          <div className="overflow-hidden rounded-lg border border-surface-border bg-card">
            {groups.map((group) => (
              <CategorySection
                key={group.category}
                category={group.category}
                builtIn={group.builtIn}
                custom={group.custom}
                canManage={isAdmin}
                onCreate={() => setCreateCategory(group.category)}
                onEdit={setEditing}
                onArchive={setPendingArchive}
              />
            ))}
          </div>
        )}
      </div>

      <StatusEditorDialog
        open={createCategory !== null}
        onOpenChange={(open) => !open && setCreateCategory(null)}
        category={createCategory}
      />
      <StatusEditorDialog
        open={Boolean(editing)}
        onOpenChange={(open) => !open && setEditing(null)}
        category={editing?.category ?? null}
        status={editing}
      />
      <ArchiveStatusDialog status={pendingArchive} onClose={() => setPendingArchive(null)} />
    </SettingsTab>
  );
}

/**
 * 单个类别区块：头部展示类别名与「新增」按钮，下方列出该类别下的内置行（只读）
 * 和自定义行（支持拖拽排序、编辑与归档）。
 */
function CategorySection({
  category,
  builtIn,
  custom,
  canManage,
  onCreate,
  onEdit,
  onArchive,
}: {
  category: IssueStatusCategory;
  builtIn: IssueStatusEntry | undefined;
  custom: IssueStatusEntry[];
  canManage: boolean;
  onCreate: () => void;
  onEdit: (status: IssueStatusEntry) => void;
  onArchive: (status: IssueStatusEntry) => void;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const labelOf = useStatusLabel(wsId);
  const reorder = useReorderIssueStatuses();

  // 用一份本地顺序数组先响应拖拽，让排序在乐观缓存写入落定前就"立刻跟手"；
  // 服务端列表一变就重新同步这份本地副本。
  const [order, setOrder] = useState(custom);
  useEffect(() => setOrder(custom), [custom]);

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
  );

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const from = order.findIndex((s) => s.id === active.id);
    const to = order.findIndex((s) => s.id === over.id);
    if (from < 0 || to < 0) return;
    const next = arrayMove(order, from, to);
    setOrder(next);
    // 只提交「活跃」行的新顺序。开着"显示已归档"时，order 里也会混入归档行；
    // 把它们一起发给服务端会被拒绝，而在那次写入改成原子操作之前，这个拒绝
    // 是在活跃行已经被重排之后才落到前端的，造成"排完又弹回"的错乱。
    // 归档行本来就是冻结的，载荷里不含它们，也正是用户看到的最终结果。
    reorder.mutate(
      { category, ordered: next.filter((entry) => !entry.archived_at) },
      {
        onError: (error) => {
          setOrder(custom);
          toast.error(
            error instanceof Error ? error.message : t(($) => $.issue_statuses.reorder_failed),
          );
        },
      },
    );
  };

  // 只有真正"动得了"的行才允许拖拽：仅有一条自定义状态时没有可交换的对象，
  // 归档行则一律冻结。
  const sortableIds = order.filter((s) => !s.archived_at).map((s) => s.id);
  const canReorder = canManage && sortableIds.length > 1;

  return (
    <section className="border-b border-surface-border last:border-b-0">
      {/* 头部只留类别名 + 它独有的一件动作。类别图标和正下方内置行渲染的是同一个图标，
          再在头部放一遍等于让眼睛读两遍早已看过的东西。 */}
      <div className="flex items-center justify-between gap-2 bg-muted/20 px-4 py-1.5">
        <span className="text-caption font-medium text-muted-foreground">
          {labelOf(category)}
        </span>
        {canManage && (
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t(($) => $.issue_statuses.add)}
                  onClick={onCreate}
                >
                  <Plus className="size-4" />
                </Button>
              }
            />
            <TooltipContent>{t(($) => $.issue_statuses.add)}</TooltipContent>
          </Tooltip>
        )}
      </div>

      <div className="divide-y divide-surface-border">
        {builtIn && (
          <BuiltInRow
            entry={builtIn}
            label={labelOf(builtIn.key)}
            behavior={t(($) => $.issue_statuses.categories[category])}
          />
        )}
        <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
          <SortableContext items={sortableIds} strategy={verticalListSortingStrategy}>
            {order.map((entry) => (
              <CustomStatusRow
                key={entry.id}
                entry={entry}
                canManage={canManage}
                canReorder={canReorder && !entry.archived_at}
                onEdit={() => onEdit(entry)}
                onArchive={() => onArchive(entry)}
              />
            ))}
          </SortableContext>
        </DndContext>
      </div>
    </section>
  );
}

/** 内置状态行：只读展示图标与名称，副行用中文类别行为句说明该状态的语义；没有操作菜单。 */
function BuiltInRow({
  entry,
  label,
  behavior,
}: {
  entry: IssueStatusEntry;
  label: string;
  behavior: string;
}) {
  return (
    <div className="flex min-h-12 items-center gap-3 px-4 py-2">
      <StatusIcon status={entry.key} category={entry.category} className="size-4" />
      <div className="min-w-0">
        <p className="truncate text-body font-medium">{label}</p>
        {/* 目录自身的描述字段是服务端种的英文种子文案，类别行为句则是同一事实的
            已翻译表述，所以展示中文那句。行上不画锁图标——内置状态本身就是
            "没有操作菜单的那一行"，挂七把锁等于把这件事说了七遍。 */}
        <p className="truncate text-caption text-muted-foreground">{behavior}</p>
      </div>
    </div>
  );
}

/**
 * 自定义状态行：可拖拽排序（handle 位于行内左侧）；管理员可通过行尾菜单编辑或归档。
 */
function CustomStatusRow({
  entry,
  canManage,
  canReorder,
  onEdit,
  onArchive,
}: {
  entry: IssueStatusEntry;
  canManage: boolean;
  canReorder: boolean;
  onEdit: () => void;
  onArchive: () => void;
}) {
  const { t } = useT("settings");
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: entry.id,
    disabled: !canReorder,
  });

  const archived = Boolean(entry.archived_at);

  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={`group/row relative flex min-h-12 items-center gap-3 bg-card px-4 py-2 ${isDragging ? "z-10 shadow-[var(--surface-shadow)]" : ""} ${archived ? "opacity-60" : ""}`}
    >
      {/* 拖拽把手放在行自身的左侧内边距里，而不是单独占一列。预留装订线会让所有行
          ——包括永远拖不了的内置行——从卡片边缘得到一致的缩进，
          这份整体缩进就是列表给人的秩序感。(MUL-6422) */}
      {canReorder && (
        <button
          type="button"
          aria-label={t(($) => $.issue_statuses.actions.reorder, { name: entry.name })}
          className="absolute left-0 top-1/2 flex w-4 -translate-y-1/2 cursor-grab justify-center text-faint-foreground opacity-0 transition-opacity group-hover/row:opacity-100 focus-visible:opacity-100 active:cursor-grabbing"
          {...attributes}
          {...listeners}
        >
          <GripVertical className="size-4" />
        </button>
      )}
      <StatusIcon
        status={entry.key}
        category={entry.category}
        color={issueStatusColor(entry)}
        className="size-4"
      />
      {/* 名称在上、描述在下，顺着阅读顺序走。旧布局把描述钉在最右侧，
          于是每个没写描述的状态都留下一列破折号，很杂乱。 */}
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-body font-medium">{entry.name}</span>
          {archived && (
            <Tooltip>
              <TooltipTrigger
                render={
                  <span className="shrink-0 rounded-full bg-muted/60 px-1.5 py-0.5 text-micro text-muted-foreground">
                    {t(($) => $.issue_statuses.archived_badge)}
                  </span>
                }
              />
              <TooltipContent>{t(($) => $.issue_statuses.archived_hint)}</TooltipContent>
            </Tooltip>
          )}
        </div>
        {entry.description && (
          <p className="truncate text-caption text-muted-foreground">{entry.description}</p>
        )}
      </div>
      {canManage && !archived && (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t(($) => $.issue_statuses.actions.open, { name: entry.name })}
              >
                <MoreHorizontal className="size-4" />
              </Button>
            }
          />
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={onEdit}>
              <Pencil className="size-4" />
              {t(($) => $.issue_statuses.actions.edit)}
            </DropdownMenuItem>
            <DropdownMenuItem variant="destructive" onClick={onArchive}>
              <Archive className="size-4" />
              {t(($) => $.issue_statuses.actions.archive)}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  );
}

/**
 * 新建 / 编辑状态的共用弹窗：传入 status 时走"编辑既有状态"，否则按传入的类别新建。
 * 编辑模式下类别字段锁定不可改。
 */
function StatusEditorDialog({
  open,
  onOpenChange,
  category,
  status,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  category: IssueStatusCategory | null;
  status?: IssueStatusEntry | null;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const labelOf = useStatusLabel(wsId);
  const create = useCreateIssueStatus();
  const update = useUpdateIssueStatus();
  const [draft, setDraft] = useState<StatusDraft>(EMPTY_DRAFT);

  const categoryItems = ALL_STATUSES.map((c) => ({
    value: c,
    label: (
      <span className="flex items-center gap-2">
        <StatusIcon status={c} category={c} className="size-3.5" />
        {labelOf(c)}
      </span>
    ),
  }));

  useEffect(() => {
    if (!open) return;
    // 每次打开弹窗都重新用待编辑状态（或空白草稿）初始化表单，避免带出上一次的残留输入。
    setDraft(
      status
        ? {
            name: status.name,
            description: status.description ?? "",
            category: status.category,
            color: status.color,
          }
        : { ...EMPTY_DRAFT, category: category ?? "todo" },
    );
  }, [status, category, open]);

  const submit = () => {
    const name = draft.name.trim();
    if (!name) return;
    const onError = (error: unknown) =>
      toast.error(
        error instanceof Error ? error.message : t(($) => $.issue_statuses.editor.save_failed),
      );

    if (status) {
      update.mutate(
        {
          id: status.id,
          name,
          description: draft.description.trim(),
          color: draft.color,
        },
        { onSuccess: () => onOpenChange(false), onError },
      );
      return;
    }
    create.mutate(
      {
        name,
        description: draft.description.trim(),
        category: draft.category,
        color: draft.color,
      },
      {
        onSuccess: (created) => {
          onOpenChange(false);
          // key 是服务端派生出来的、API 和 CLI 唯一认的标识，所以新建必须把生成了什么说清楚。
          // 当名字里没有任何可转 slug 的 ASCII 字符时，生成的 key 无法从名字反推回来
          // ——「客户确认」会变成 in_review_2（MUL-6749）——若一声不吭就关闭，
          // 管理员只能重新打开那一行才能查到。弹窗马上要关了，toast 是唯一还看得见的出口。
          if (created?.key) {
            toast.success(t(($) => $.issue_statuses.editor.created, { key: created.key }));
          }
        },
        onError,
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {status
              ? t(($) => $.issue_statuses.editor.edit_title)
              : t(($) => $.issue_statuses.editor.create_title)}
          </DialogTitle>
          <DialogDescription>
            {t(($) => $.issue_statuses.editor.behavior_hint, {
              category: labelOf(draft.category),
            })}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-5 py-2">
          <div className="space-y-2">
            <FieldLabel htmlFor="status-name">
              {t(($) => $.issue_statuses.editor.name)}
            </FieldLabel>
            <Input
              id="status-name"
              autoFocus
              maxLength={64}
              value={draft.name}
              onChange={(event) =>
                setDraft((current) => ({ ...current, name: event.target.value }))
              }
              placeholder={t(($) => $.issue_statuses.editor.name_placeholder)}
            />
            {/* key 是 API 与 CLI 认的字符串，且重命名状态并不会改变 key，所以它总得
                有个能读到的地方。放在这里而不是给每行都贴个徽标：列表是用来扫名字的，
                每行旁边再放一串 slug 就把列表变成了一张内部细节表。(MUL-6422) */}
            {status && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.issue_statuses.editor.key_hint, { key: status.key })}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <FieldLabel>{t(($) => $.issue_statuses.editor.category)}</FieldLabel>
            {/* 创建后类别不可再改：改了等于悄悄改写每个已挂在该状态上的 issue 的平台行为。 */}
            <Select
              items={categoryItems}
              value={draft.category}
              onValueChange={(value) =>
                value &&
                setDraft((current) => ({ ...current, category: value as IssueStatusCategory }))
              }
              disabled={Boolean(status)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {categoryItems.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-caption text-muted-foreground">
              {status
                ? t(($) => $.issue_statuses.editor.category_locked)
                : t(($) => $.issue_statuses.categories[draft.category])}
            </p>
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="status-description">
              {t(($) => $.issue_statuses.editor.description)}
            </FieldLabel>
            <Textarea
              id="status-description"
              rows={3}
              maxLength={256}
              value={draft.description}
              onChange={(event) =>
                setDraft((current) => ({ ...current, description: event.target.value }))
              }
              placeholder={t(($) => $.issue_statuses.editor.description_placeholder)}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel>{t(($) => $.issue_statuses.editor.color)}</FieldLabel>
            <ColorPicker
              value={draft.color}
              onChange={(color) => setDraft((current) => ({ ...current, color }))}
              trigger={
                <button
                  type="button"
                  aria-label={t(($) => $.issue_statuses.editor.color)}
                  className="flex h-9 items-center gap-2.5 rounded-md border border-surface-border px-2.5 transition-colors hover:bg-surface-hover"
                >
                  <span className="size-5 rounded-full" style={{ backgroundColor: draft.color }} />
                  <span className="font-mono text-caption uppercase text-muted-foreground">
                    {draft.color}
                  </span>
                </button>
              }
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t(($) => $.issue_statuses.editor.cancel)}
          </Button>
          <Button
            onClick={submit}
            disabled={!draft.name.trim() || create.isPending || update.isPending}
          >
            {create.isPending || update.isPending
              ? t(($) => $.issue_statuses.editor.saving)
              : t(($) => $.issue_statuses.editor.save)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** 归档确认弹窗：向用户讲清"归档只是退出未来的新指派，旧 issue 不受影响"，以免被当成删除。 */
function ArchiveStatusDialog({
  status,
  onClose,
}: {
  status: IssueStatusEntry | null;
  onClose: () => void;
}) {
  const { t } = useT("settings");
  const archive = useArchiveIssueStatus();
  return (
    <AlertDialog open={Boolean(status)} onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(($) => $.issue_statuses.archive_dialog.title)}</AlertDialogTitle>
          {/* 归档只是让状态退出"未来指派"：已挂其上的 issue 仍保留该状态、仍按类别规则运行——
              这一点必须写明，否则这段描述读起来会像"删除"。 */}
          <AlertDialogDescription>
            {t(($) => $.issue_statuses.archive_dialog.description, {
              name: status?.name ?? "",
            })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>
            {t(($) => $.issue_statuses.archive_dialog.cancel)}
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={() => {
              if (!status) return;
              archive.mutate(status.id, {
                onSuccess: onClose,
                onError: (error) =>
                  toast.error(
                    error instanceof Error
                      ? error.message
                      : t(($) => $.issue_statuses.archive_dialog.failed),
                  ),
              });
            }}
          >
            {t(($) => $.issue_statuses.archive_dialog.confirm)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
