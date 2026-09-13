// 给旧式（pre-Table）测试夹具用的过渡适配器。生产代码永远不会 import 这个模块；
// 它让既有 surface 测试既能保留小型「按状态分内存」数据源，又能断言新的请求约定。
import { ALL_STATUSES } from "@multica/core/issues/config";
import type {
  Issue,
  IssueTableGroupDescriptor,
  IssueStatus,
  IssueTableFacetsRequest,
  IssueTableGroupsRequest,
  IssueTableQuerySpec,
  IssueTableRowsRequest,
  ListIssuesParams,
  ListIssuesResponse,
} from "@multica/core/types";

type LegacyListIssues = (
  params?: ListIssuesParams,
) => Promise<ListIssuesResponse>;

/** 一个持有多条运行中问题任务的智能体，按「工作中智能体」投影报告。
 *  `issue_ids` 可能指向所查询 surface 之外的问题——facet 预期会丢弃它们，
 *  这正是 MUL-5525 的意义。 */
export interface WorkingTaskFixture {
  id: string;
  issue_ids: readonly string[];
}

/**
 * `working_agents` facet 的数据。`rows` 直接读取而不是走旧式 list 接口：
 * 有多个测试断言 Table surface 从不触碰那个接口，一个又去调用它的 facet 替身
 * 会让这些测试以错误的理由通过或失败。
 */
export interface WorkingAgentsFixture {
  rows: () => readonly Issue[];
  agents: () => readonly WorkingTaskFixture[];
}

/**
 * 状态分组的「分组 key 轴」。看板/列表/泳道 surface 按「分类」分页
 * （`status_category:<category>`）；表格仍按具体状态 key 分组。这个夹具只装
 * 内置状态——它们 key 即分类——所以两个轴选中同一批行，只是 key 形状不同。（MUL-6243）
 */
function statusAxis(group: { kind: string }): string {
  return group.kind === "status_category" ? "status_category" : "status";
}

function secondaryAxis(group: { kind: string; secondary?: string }): string {
  return group.secondary === "status_category" ? "status_category" : "status";
}

function legacyParamsForStatus(
  query: IssueTableQuerySpec,
  status: IssueStatus,
): ListIssuesParams {
  const scope = query.scope;
  return {
    status,
    limit: 50,
    offset: 0,
    ...(scope.kind === "project" ? { project_id: scope.project_id } : {}),
    ...(scope.kind === "assignee" && scope.actor
      ? {
          assignee_type: scope.actor.type,
          assignee_id: scope.actor.id,
        }
      : {}),
    ...(scope.kind === "creator" && scope.actor
      ? {
          creator_type: scope.actor.type,
          creator_id: scope.actor.id,
        }
      : {}),
    ...(query.search ? { search: query.search } : {}),
  };
}

async function rowsForStatus(
  listIssues: LegacyListIssues,
  query: IssueTableQuerySpec,
  status: IssueStatus,
) {
  if (
    query.filters.statuses &&
    !query.filters.statuses.includes(status)
  ) {
    return [];
  }
  const response = await listIssues(legacyParamsForStatus(query, status));
  return response.issues.filter((issue) => {
    if (
      query.filters.include_sub_issues === false &&
      issue.parent_issue_id !== null
    ) {
      return false;
    }
    return issue.status === status;
  });
}

async function allRows(
  listIssues: LegacyListIssues,
  query: IssueTableQuerySpec,
) {
  const rows = await Promise.all(
    ALL_STATUSES.map((status) =>
      rowsForStatus(
        listIssues,
        { ...query, filters: { ...query.filters, statuses: undefined } },
        status,
      ),
    ),
  );
  return rows.flat();
}

// working-agents facet 只丢弃「它自己」这一维，所以 spec 里的其他筛选依然生效。
// 与 `rowsForStatus` 施行的两个谓词保持一致。
function workingAgentFacetValues(
  query: IssueTableQuerySpec,
  fixture: WorkingAgentsFixture,
) {
  const statuses = query.filters.statuses;
  const visible = new Set(
    fixture
      .rows()
      .filter((issue) => !statuses || statuses.includes(issue.status))
      .filter(
        (issue) =>
          query.filters.include_sub_issues !== false ||
          issue.parent_issue_id === null,
      )
      .map((issue) => issue.id),
  );
  return fixture.agents().flatMap((agent) => {
    const count = agent.issue_ids.filter((id) => visible.has(id)).length;
    return count > 0 ? [{ key: agent.id, count }] : [];
  });
}

function primaryDescriptor(
  issue: Issue,
  primary: "assignee" | "project" | "parent",
  issueById: ReadonlyMap<string, Issue>,
): Omit<IssueTableGroupDescriptor, "count" | "secondary_groups"> {
  if (primary === "assignee") {
    const actor =
      issue.assignee_type && issue.assignee_id
        ? { type: issue.assignee_type, id: issue.assignee_id }
        : null;
    return {
      key: actor
        ? `assignee:${actor.type}:${actor.id}`
        : "assignee:unassigned",
      value: { kind: "assignee", actor },
    };
  }
  if (primary === "project") {
    return {
      key: issue.project_id ? `project:${issue.project_id}` : "project:none",
      value: { kind: "project", project_id: issue.project_id },
    };
  }
  const parent = issue.parent_issue_id
    ? issueById.get(issue.parent_issue_id) ?? null
    : null;
  return {
    key: issue.parent_issue_id
      ? `parent:${issue.parent_issue_id}`
      : "parent:none",
    value: {
      kind: "parent",
      parent_id: issue.parent_issue_id,
      parent: parent
        ? {
            id: parent.id,
            number: parent.number,
            identifier: parent.identifier,
            title: parent.title,
            status: parent.status,
          }
        : null,
      value_state: issue.parent_issue_id
        ? parent
          ? "value"
          : "unavailable"
        : "unset",
    },
  };
}

/**
 * 给旧式（pre-Table）测试夹具用的过渡适配器：把旧的 `listIssues` 数据源包装成
 * Table 请求约定的三个方法。生产代码从不 import 本模块。
 * @param listIssues - 旧式的按状态列问题的接口。
 * @param workingAgents - 服务端「问题 ↔ 运行中任务」联接的替身。提供它才能演练
 *   `working_agents` facet；不提供时 facet 回答为空，等价于一个什么都没在跑的工作区。
 */
export function statusTableMethodsFromLegacy(
  listIssues: LegacyListIssues,
  // 服务端「问题 ↔ 运行中任务」联接的替身。提供它才能演练 `working_agents` facet；
  // 不提供时 facet 回答为空，等价于一个什么都没在跑的工作区。
  workingAgents: WorkingAgentsFixture = { rows: () => [], agents: () => [] },
) {
  return {
    listIssueTableGroups: async (request: IssueTableGroupsRequest) => {
      if (request.group.kind === "compound") {
        const issues = await allRows(listIssues, request.query);
        const issueById = new Map(issues.map((issue) => [issue.id, issue]));
        const grouped = new Map<
          string,
          {
            descriptor: ReturnType<typeof primaryDescriptor>;
            issues: Issue[];
          }
        >();
        for (const issue of issues) {
          const descriptor = primaryDescriptor(
            issue,
            request.group.primary,
            issueById,
          );
          const current = grouped.get(descriptor.key) ?? {
            descriptor,
            issues: [],
          };
          current.issues.push(issue);
          grouped.set(descriptor.key, current);
        }
        return {
          query_fingerprint: "test",
          total: issues.length,
          groups: Array.from(grouped.values(), ({ descriptor, issues }) => ({
            ...descriptor,
            count: issues.length,
            secondary_groups: ALL_STATUSES.flatMap((status) => {
              const count = issues.filter((issue) => issue.status === status).length;
              return count
                ? [{
                    key: `compound:${descriptor.key}:${secondaryAxis(request.group)}:${status}`,
                    value: { kind: "status" as const, status },
                    count,
                  }]
                : [];
            }),
          })),
          next_cursor: null,
        };
      }
      const groups = await Promise.all(
        ALL_STATUSES.map(async (status) => ({
          status,
          issues: await rowsForStatus(listIssues, request.query, status),
        })),
      );
      const nonEmpty = groups.filter(({ issues }) => issues.length > 0);
      return {
        query_fingerprint: "test",
        total: nonEmpty.reduce((sum, group) => sum + group.issues.length, 0),
        groups: nonEmpty.map(({ status, issues }) => ({
          key: `${statusAxis(request.group)}:${status}`,
          value: { kind: "status" as const, status },
          count: issues.length,
        })),
        next_cursor: null,
      };
    },
    listIssueTableRows: async (request: IssueTableRowsRequest) => {
      if (request.group.kind === "compound") {
        const primary = request.group.primary;
        const axis = `:${secondaryAxis(request.group)}:`;
        const marker = request.group_key?.lastIndexOf(axis) ?? -1;
        const status =
          marker >= 0 ? request.group_key?.slice(marker + axis.length) : undefined;
        const primaryKey =
          marker >= 0
            ? request.group_key?.slice("compound:".length, marker)
            : undefined;
        const all = await allRows(listIssues, request.query);
        const issueById = new Map(all.map((issue) => [issue.id, issue]));
        const issues = all.filter((issue) => {
          const descriptor = primaryDescriptor(
            issue,
            primary,
            issueById,
          );
          return descriptor.key === primaryKey && issue.status === status;
        });
        return {
          query_fingerprint: "test",
          group_key: request.group_key,
          parent_id: request.parent_id,
          total: 0,
          rows: issues.map((issue) => ({
            issue,
            direct_child_count: 0,
          })),
          branch_total: issues.length,
          next_cursor: null,
        };
      }
      const rawStatus = request.group_key?.replace(/^status(_category)?:/, "");
      const status = ALL_STATUSES.find((value) => value === rawStatus);
      const issues = status
        ? await rowsForStatus(listIssues, request.query, status)
        : [];
      return {
        query_fingerprint: "test",
        group_key: request.group_key,
        parent_id: request.parent_id,
        total: 0,
        rows: issues.map((issue) => ({
          issue,
          direct_child_count: 0,
        })),
        branch_total: issues.length,
        next_cursor: null,
      };
    },
    listIssueTableFacets: async (request: IssueTableFacetsRequest) => {
      // 只有在真正请求了状态 facet 时才去碰旧式 list 接口：Table surface 会请求其他
      // facet，同时断言那个接口永远不会被调用。
      const groups = request.facets.some((facet) => facet.kind === "status")
        ? await Promise.all(
            ALL_STATUSES.map(async (status) => ({
              status,
              issues: await rowsForStatus(
                listIssues,
                {
                  ...request.query,
                  filters: {
                    ...request.query.filters,
                    statuses: undefined,
                  },
                },
                status,
              ),
            })),
          )
        : [];
      return {
        query_fingerprint: "test",
        total: groups.reduce((sum, group) => sum + group.issues.length, 0),
        facets: request.facets.map((facet) => ({
          ...facet,
          values:
            facet.kind === "status"
              ? groups
                  .filter(({ issues }) => issues.length > 0)
                  .map(({ status, issues }) => ({
                    key: status,
                    count: issues.length,
                  }))
              : facet.kind === "working_agents"
                ? workingAgentFacetValues(request.query, workingAgents)
                : [],
        })),
      };
    },
  };
}
