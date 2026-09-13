// 本文件定义「议题状态目录」类型：一个 workspace 用到的全部议题状态，
// 含 7 个内建状态与自定义状态（MUL-6243）。

import type { IssueStatusCategory } from "./issue";

// IssueStatusCategory 之所以定义在 ./issue（IssueStatus 旁边），是因为两者
// 只有放在一起读才自洽：一个类别是 7 个内建 key 之一，而一个状态 key 要么是
// 类别、要么是 workspace 的自定义 key。在这里重新导出，让状态目录的消费方
// 能从一个地方同时引入二者。（MUL-6243）
export type { IssueStatusCategory } from "./issue";

/**
 * 一个 workspace 的议题状态目录（MUL-6243）。
 *
 * 7 个类别与 7 个内建状态一一对应：类别的值本身就是它的规范状态 key。
 * 自定义状态指定一个类别并完整继承该规范状态的平台行为——`todo` 类别的
 * 自定义状态与 Todo 一样触发 agent，`in_review` 类别里的自定义状态与
 * In Review 一样终结一次 autopilot 运行。
 *
 * 因此这里没有单独的「行为」字段：类别就是行为。
 */

/** 状态目录里的单个状态条目。 */
export interface IssueStatusEntry {
  id: string;
  workspace_id: string;
  /**
   * 稳定的机器句柄，创建后不可变。这就是存在 `issue.status` 里的值、
   * `multica issue status` 接受的值、agent 指令里引用的值——所以它不随
   * `name` 的重命名而变。
   */
  key: string;
  /** 面向用户的展示名。自定义状态可改；内建状态锁定。 */
  name: string;
  description: string;
  category: IssueStatusCategory;
  /** "#rrggbb"。 */
  color: string;
  /**
   * 7 个内建状态为 true。它们不能重命名、改色、归档或改类别：每个内建状态
   * 就是其类别的规范定义，且默认 workspace 必须对所有没打开过状态设置的
   * 用户看起来一致。
   */
  is_system: boolean;
  /** 类别内的排序位置。 */
  position: number;
  archived_at: string | null;
  created_at: string;
  updated_at: string;
}

/** 状态目录列表接口响应。 */
export interface ListIssueStatusesResponse {
  statuses: IssueStatusEntry[];
  /** 7 个类别，按展示顺序排列。 */
  categories: IssueStatusCategory[];
  total: number;
}

/** 新建自定义议题状态的请求体。 */
export interface CreateIssueStatusRequest {
  /** 可选；省略时由 `name` 推导。创建后不可变。 */
  key?: string;
  name: string;
  description?: string;
  category: IssueStatusCategory;
  color: string;
}

/**
 * `key` 与 `category` 刻意缺失——两者都不可变。改类别会静默改写每个已在
 * 该状态上的议题的机器语义；改 key 会让它们孤立失联。
 */
export interface UpdateIssueStatusRequest {
  name?: string;
  description?: string;
  color?: string;
  position?: number;
}
