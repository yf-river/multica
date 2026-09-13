// 本文件：table-view-model —— 表格视图的纯展示模型与导出工具。
// 把表格要渲染的行抽象成可序列化的"显示行"（分组行/问题行/骨架行/
// 加载更多行），并负责选中区间的展开、冻结行快照的刷新，以及把表格内容
// 拼成 CSV（含防公式注入转义）。不含任何组件，供表格视图与导出逻辑共用。

import { propertyIdFromViewKey } from "@multica/core/issues/stores/view-store";
import type {
  TableCalculation,
  TableColumnKey,
} from "@multica/core/issues/stores/view-store";
import type {
  Issue,
  IssuePropertyValue,
} from "@multica/core/types";

/** 导出必须"失败即关闭"：当分页的表格响应无法证明完整查询窗口已被收集时
 * 抛出本错误。UI 把它翻译成用户可读的文案，而不暴露协议细节。 */
export class IssueTableExportIntegrityError extends Error {
  constructor() {
    super("Table export response was incomplete");
    this.name = "IssueTableExportIntegrityError";
  }
}

/**
 * 表格视图渲染的一行。调用方（useMemo 计算展示行）只读消费，不直接修改。
 */
export type IssueTableDisplayRow =
  | {
      kind: "group";
      key: string;
      label: string;
      count: number;
      collapsed: boolean;
    }
  | {
      kind: "issue";
      key: string;
      issue: Issue;
      depth: number;
      hasChildren: boolean;
      collapsed: boolean;
    }
  // 代表"还没到货"的行。冷加载在表格自己的网格里渲染这些行，而不是把整个
  // 表面换成通用占位：列、列宽和表头在数据到达前就已知道，只需要为行做
  // 站位，且替换时布局不会跳动。
  | { kind: "skeleton"; key: string }
  // 携带状态而不是成品文案：行通过 ListLoadMoreFooter 渲染——与看板/列表/
  // 泳道共用的同一个页脚，每种状态的话术与样式都由它负责。
  | {
      kind: "load_more";
      key: string;
      state: "loading" | "has_more" | "error" | "end";
      // 超过这个数量的行意味着该分支确实发生了分页——这是决定"滚到末尾
      // 是否值得标记"的依据。
      total: number;
      onLoad?: () => void;
    };

/**
 * 返回从 `anchorId` 到 `targetId`（含两端）的 issue id 区间，用于点击选中
 * 时的范围展开。锚点或目标不在列表里时返回 null。
 */
export function getIssueTableSelectionRange(
  issueIds: string[],
  anchorId: string | null,
  targetId: string,
): string[] | null {
  if (!anchorId) return null;
  const anchorIndex = issueIds.indexOf(anchorId);
  const targetIndex = issueIds.indexOf(targetId);
  if (anchorIndex === -1 || targetIndex === -1) return null;

  const start = Math.min(anchorIndex, targetIndex);
  const end = Math.max(anchorIndex, targetIndex);
  return issueIds.slice(start, end + 1);
}

/**
 * 刷新冻结行快照里的 issue 对象。单元格编辑弹层打开期间，表格渲染的是
 * 结构快照（行顺序、分组、嵌套保持不变，让弹层的锚点行不会在交互中途被
 * 虚拟化窗口重排出去），但行内的"值"必须保持实时——例如多选切换在弹层
 * 打开时提交，其勾选必须反映乐观缓存。从实时窗口删除的问题保留其过期
 * 快照对象；编辑器一关闭，结构即会追上。
 */
export function refreshFrozenTableRows(
  snapshot: IssueTableDisplayRow[],
  issueById: ReadonlyMap<string, Issue>,
): IssueTableDisplayRow[] {
  return snapshot.map((row) => {
    if (row.kind !== "issue") return row;
    const live = issueById.get(row.issue.id);
    return live && live !== row.issue ? { ...row, issue: live } : row;
  });
}
function columnValue(
  issue: Issue,
  columnKey: TableColumnKey,
): IssuePropertyValue | string | number | null | undefined {
  const propertyId = propertyIdFromViewKey(columnKey);
  if (propertyId) return issue.properties[propertyId];
  switch (columnKey) {
    case "identifier":
      return issue.identifier;
    case "title":
      return issue.title;
    case "status":
      return issue.status;
    case "priority":
      return issue.priority;
    case "assignee":
      return issue.assignee_id;
    case "labels":
      return issue.labels?.map((label) => label.name).join(", ");
    case "project":
      return issue.project_id;
    case "start_date":
      return issue.start_date;
    case "due_date":
      return issue.due_date;
    case "created_at":
      return issue.created_at;
    case "updated_at":
      return issue.updated_at;
    case "child_progress":
      return undefined;
    case "creator":
      return issue.creator_id;
  }
  return undefined;
}

/**
 * 对一列问题在给定列上做数值聚合：`count` 统计非空值个数；`sum` / `average`
 * 只统计数值型值，没有可聚合的数值时返回 null。`none` 返回 null（调用方用
 * 它表示"不显示聚合"）。
 */
export function calculateIssueTableColumn(
  issues: Issue[],
  columnKey: TableColumnKey,
  calculation: TableCalculation,
) {
  if (calculation === "none") return null;
  const values = issues
    .map((issue) => columnValue(issue, columnKey))
    .filter((value) => value !== undefined && value !== null && value !== "");
  if (calculation === "count") return values.length;
  const numbers = values.filter((value): value is number => typeof value === "number");
  if (numbers.length === 0) return null;
  const sum = numbers.reduce((total, value) => total + value, 0);
  return calculation === "sum" ? sum : sum / numbers.length;
}

function escapeCsvCell(value: unknown) {
  const raw = value == null ? "" : String(value);
  // 电子表格应用会把以这些字符开头的字符串单元格当作公式执行。给文本
  //（包括表头）加前导撇号，让导出的用户可控标题/属性始终是数据。
  // 真实数字保留数字形态——负数值不是被注入的公式字符串。
  const text =
    typeof value === "string" && /^[=+\-@\t\r]/.test(raw)
      ? `'${raw}`
      : raw;
  return /[",\n\r]/.test(text) ? `"${text.replaceAll('"', '""')}"` : text;
}

/**
 * 把表头 + 数据行拼成 CSV 文本（CRLF 行尾、RFC 4180 引号转义、防公式注入）。
 */
export function buildIssueTableCsv(
  headers: string[],
  rows: unknown[][],
) {
  return [headers, ...rows]
    .map((row) => row.map(escapeCsvCell).join(","))
    .join("\r\n");
}
