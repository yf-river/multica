// 运行复盘的「导出 Excel」逻辑：把节点数据、产物链接、RAW 交互信息整理成
// xlsx 用的表结构（值矩阵 + 超链接坐标）。

import { api } from "@multica/core/api";
import { resolvePublicFileUrlWithBase } from "@multica/core/workspace/avatar-url";
import type { AgentTaskArtifact, Issue, IssueExecutionTreeResponse } from "@multica/core/types";
import { runReviewTotalDurationMs } from "./run-review-live";
import type { RunReviewEventRowData } from "./run-review-events";
import {
  agentNodeDisplayLabel,
  artifactDisplayName,
  buildAgentNodeRows,
  buildChildLanes,
  dedupeArtifacts,
  nodeTokenTotal,
} from "./run-review-timeline";
import {
  cacheReuseRate,
  firstNonEmpty,
  formatDateTime,
  formatDuration,
  formatJSON,
  formatNumber,
  formatPercent,
} from "./run-review-format";

type XlsxCellValue = string | number | boolean | null | undefined;

interface XlsxHyperlink {
  row: number;
  col: number;
  target: string;
  tooltip?: string;
}

/** 一张 Excel 工作表的描述：表名 + 值矩阵，可选超链接列表与列宽。 */
export interface XlsxSheetSpec {
  name: string;
  rows: XlsxCellValue[][];
  hyperlinks?: XlsxHyperlink[];
  columnWidths?: number[];
}

/**
 * 拼出产物（artifact）的下载链接：有 id 走附件下载接口，否则退回它的
 * download_url / markdown_url，并解析成可公开访问的绝对/相对地址。
 */
export function artifactDownloadHref(artifact: AgentTaskArtifact, baseUrl?: string) {
  const endpoint = artifact.id
    ? `/api/attachments/${encodeURIComponent(artifact.id)}/download`
    : firstNonEmpty(artifact.download_url, artifact.markdown_url);
  const resolvedBaseUrl = baseUrl ?? (typeof api.getBaseUrl === "function" ? api.getBaseUrl() : "");
  return resolvePublicFileUrlWithBase(endpoint, resolvedBaseUrl) ?? endpoint;
}

/** 产物在工作表超链接里要用的绝对地址（导出文件需要绝对 URL 才能点）。 */
function artifactXlsxHyperlinkHref(artifact: AgentTaskArtifact, baseUrl?: string) {
  const resolvedBaseUrl = baseUrl ?? defaultXlsxHyperlinkBaseUrl();
  return toAbsoluteHyperlink(artifactDownloadHref(artifact, resolvedBaseUrl), resolvedBaseUrl);
}

/** 超链接兜底基准地址：优先 API 地址，其次当前页面 origin。 */
function defaultXlsxHyperlinkBaseUrl() {
  const apiBaseUrl = typeof api.getBaseUrl === "function" ? api.getBaseUrl() : "";
  if (isAbsoluteUrl(apiBaseUrl)) return apiBaseUrl;
  if (typeof window !== "undefined" && window.location.origin) return window.location.origin;
  return apiBaseUrl;
}

/** 把相对地址拼成绝对地址；本就是绝对地址或拼不了则原样返回。 */
function toAbsoluteHyperlink(href: string | null | undefined, baseUrl: string) {
  if (!href) return "";
  if (isAbsoluteUrl(href)) return href;
  if (!baseUrl) return href;
  try {
    return new URL(href, baseUrl).toString();
  } catch {
    return href;
  }
}

/** 判断字符串是否已是绝对 URL（带 scheme，如 http:）。 */
function isAbsoluteUrl(value: string | null | undefined) {
  return !!value && /^[a-z][a-z\d+.-]*:/i.test(value);
}
/**
 * 生成复盘 Excel 的「节点数据」与「产物链接」两张表：汇总本次执行的关键耗时与
 * Token 指标，并把每个执行节点的产物整理成带下载链接的清单。
 *
 * @param _issue 任务本身（当前未被使用，保留作签名约定）
 * @param summary 本次执行的摘要（耗时 / Token / 轮次等）
 * @param agentRows 按 Agent 节点聚合出的行数据
 * @param _childLanes 子泳道数据（当前未被使用）
 * @returns 可直接交给 xlsx 导出器的表描述列表
 */
export function buildRunReviewNodeXlsxSheets(
  _issue: Issue,
  summary: IssueExecutionTreeResponse["issue_summary"] | undefined,
  agentRows: ReturnType<typeof buildAgentNodeRows>,
  _childLanes: ReturnType<typeof buildChildLanes>,
): XlsxSheetSpec[] {
  const summaryInputTokens = summary?.total_input_tokens ?? 0;
  const summaryOutputTokens = summary?.total_output_tokens ?? 0;
  const summaryCacheReadTokens = summary?.total_cache_read_tokens ?? 0;
  const summaryCacheWriteTokens = summary?.total_cache_write_tokens ?? 0;
  const totalToken = (summary?.total_input_tokens ?? 0) +
    (summary?.total_output_tokens ?? 0) +
    (summary?.total_cache_read_tokens ?? 0) +
    (summary?.total_cache_write_tokens ?? 0);
  const agentExecutionDurationMs = summary?.agent_execution_duration_ms ?? summary?.total_duration_ms ?? 0;
  const humanConfirmationDuration = summary?.human_confirmation_duration_ms == null
    ? "未记录"
    : formatDuration(summary.human_confirmation_duration_ms);
  const childIssueWaitDuration = summary?.child_issue_wait_duration_ms == null
    ? "未记录"
    : formatDuration(summary.child_issue_wait_duration_ms);
  const rows: XlsxCellValue[][] = [
    [
      "总耗时",
      "Agent 执行耗时",
      "人工确认耗时",
      "子任务等待耗时",
      "总 Token",
      "输入 Token",
      "输出 Token",
      "缓存读 Token",
      "缓存写 Token",
      "缓存命中率",
      "执行轮次",
    ],
    [
      formatDuration(runReviewTotalDurationMs(summary)),
      formatDuration(agentExecutionDurationMs),
      humanConfirmationDuration,
      childIssueWaitDuration,
      formatNumber(totalToken),
      formatNumber(summaryInputTokens),
      formatNumber(summaryOutputTokens),
      formatNumber(summaryCacheReadTokens),
      formatNumber(summaryCacheWriteTokens),
      formatPercent(cacheReuseRate(summaryCacheReadTokens, summaryCacheWriteTokens)),
      formatNumber(summary?.agent_turn_count ?? 0),
    ],
    [],
    [
      "节点",
      "Agent",
      "开始时间",
      "结束时间",
      "执行耗时",
      "Token 合计",
      "输入 Token",
      "输出 Token",
      "缓存读 Token",
      "缓存写 Token",
      "缓存命中率",
      "执行轮次",
      "产物",
    ],
  ];
  const hyperlinks: XlsxHyperlink[] = [];
  const artifactRows: XlsxCellValue[][] = [["节点", "Agent", "产物", "链接"]];
  const artifactHyperlinks: XlsxHyperlink[] = [];

  for (const row of agentRows) {
    const node = row.node;
    const artifacts = dedupeArtifacts(node.artifacts ?? []);
    const baseCells: XlsxCellValue[] = [
      agentNodeDisplayLabel(row),
      node.agent_name ?? row.key,
      formatDateTime(node.started_at),
      formatDateTime(node.completed_at),
      formatDuration(node.duration_ms ?? 0),
      formatNumber(nodeTokenTotal(node)),
      formatNumber(node.input_tokens ?? 0),
      formatNumber(node.output_tokens ?? 0),
      formatNumber(node.cache_read_tokens ?? 0),
      formatNumber(node.cache_write_tokens ?? 0),
      formatPercent(cacheReuseRate(node.cache_read_tokens ?? 0, node.cache_write_tokens ?? 0)),
      formatNumber(node.agent_turn_count ?? 0),
    ];
    if (!artifacts.length) {
      rows.push([...baseCells, "-"]);
      continue;
    }
    const nodeRowIndex = rows.length;
    rows.push([...baseCells, artifacts.map(artifactDisplayName).join("\n")]);
    const firstArtifact = artifacts[0];
    const firstArtifactHref = firstArtifact ? artifactXlsxHyperlinkHref(firstArtifact) : "";
    if (firstArtifact && firstArtifactHref) {
      hyperlinks.push({
        row: nodeRowIndex,
        col: 12,
        target: firstArtifactHref,
        tooltip: artifactDisplayName(firstArtifact),
      });
    }
    for (const artifact of artifacts) {
      const artifactRowIndex = artifactRows.length;
      const artifactName = artifactDisplayName(artifact);
      const href = artifactXlsxHyperlinkHref(artifact);
      artifactRows.push([agentNodeDisplayLabel(row), node.agent_name ?? row.key, artifactName, href]);
      if (href) {
        artifactHyperlinks.push({
          row: artifactRowIndex,
          col: 2,
          target: href,
          tooltip: artifactName,
        });
      }
    }
  }

  return [{
    name: "节点数据",
    rows,
    hyperlinks,
    columnWidths: [24, 22, 18, 18, 12, 14, 14, 14, 14, 14, 14, 12, 32],
  }, {
    name: "产物链接",
    rows: artifactRows,
    hyperlinks: artifactHyperlinks,
    columnWidths: [24, 22, 28, 72],
  }];
}

/**
 * 生成复盘 Excel 的「RAW 交互信息」表：把事件流逐条展开成一行（含类型、耗时、
 * Token、摘要、详情与原始 JSON）。
 *
 * @param eventRows 已排版好的复盘事件行数据
 * @returns 可直接交给 xlsx 导出器的表描述列表
 */
export function buildRunReviewRawEventsXlsxSheets(eventRows: RunReviewEventRowData[]): XlsxSheetSpec[] {
  const headers = [
    "id",
    "类型",
    "分类",
    "时间",
    "时间戳(ms)",
    "任务ID",
    "来源",
    "对象",
    "标题",
    "结果",
    "严重级别",
    "耗时(ms)",
    "Token合计",
    "摘要",
    "详情",
    "元数据",
    "原始来源",
    "raw_json",
    "linked_raw_json",
  ];
  const rows: XlsxCellValue[][] = [
    headers,
    ...eventRows.map((event): XlsxCellValue[] => [
      event.id,
      event.kind,
      event.category,
      event.timeLabel,
      event.timestampMs,
      event.taskId ?? "",
      event.sourceLabel,
      event.object,
      event.title,
      event.outcome,
      event.severity,
      event.durationMs,
      event.tokenTotal,
      event.summary,
      event.detail,
      event.metadataDetail,
      event.rawSourceLabel ?? "",
      event.rawPayload === undefined ? "" : formatJSON(event.rawPayload),
      event.linkedRawPayloads?.length ? formatJSON(event.linkedRawPayloads) : "",
    ]),
  ];
  return [{
    name: "RAW 交互信息",
    rows,
    columnWidths: [28, 14, 16, 18, 14, 28, 18, 26, 24, 16, 12, 14, 14, 42, 52, 52, 18, 60, 60],
  }];
}
