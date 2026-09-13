// 本文件定义「自定义议题属性」相关类型（MUL-4463）：workspace 定义在议题上
// 的类型化字段。定义存于 workspace 目录（仅 owner/admin 可管理）；值按定义 id
// 为键存在每个议题的「值袋」里，因此重命名定义永远不碰议题行。
//
// 值按定义类型化：select 存选项 id，multi_select 存选项 id 数组（按配置顺序），
// date 存 "YYYY-MM-DD" 字符串，checkbox 存布尔，number 存数字，text/url 存
// 字符串，actor 存 "member:<user_id>" 引用串，multi_actor 存它们的数组（插入
// 顺序）。

/** 自定义属性的取值类型。 */
export type IssuePropertyType =
  | "text"
  | "number"
  | "select"
  | "multi_select"
  | "date"
  | "checkbox"
  | "url"
  | "actor"
  | "multi_actor";

/** 全部已知的属性类型列表。 */
export const ISSUE_PROPERTY_TYPES: IssuePropertyType[] = [
  "text",
  "number",
  "select",
  "multi_select",
  "date",
  "checkbox",
  "url",
  "actor",
  "multi_actor",
];

/** `type` 是否为已知的属性类型。 */
export function isKnownPropertyType(type: string): type is IssuePropertyType {
  return (ISSUE_PROPERTY_TYPES as string[]).includes(type);
}

/**
 * actor 属性（MUL-6286）引用一个 workspace 成员。经办人字段还接受 agent 与
 * squad；actor 属性刻意不收——agent 引用会牵入 agent 可见性规则，squad 是
 * 路由目标而不是人。
 *
 * 存储形式是 "<kind>:<uuid>"，因此今后放宽该联合无需迁移，也无需新属性类型。
 */
export type IssuePropertyActorKind = "member";

/** 目前仅支持 kind = "member"。 */
export const ISSUE_PROPERTY_ACTOR_KINDS: IssuePropertyActorKind[] = ["member"];

/** 单个 multi_actor 值的数量上限；与服务端上限一致。 */
export const MAX_ISSUE_PROPERTY_ACTOR_VALUES = 20;

/** 解析后的 actor 引用。 */
export interface IssuePropertyActorRef {
  kind: IssuePropertyActorKind;
  /** 成员以 `user_id` 引用，与议题经办人对的形式一致。 */
  id: string;
}

/** `type` 是否为 actor 型属性。 */
export function isActorPropertyType(type: string): boolean {
  return type === "actor" || type === "multi_actor";
}

/**
 * 议题筛选菜单为「有值 + 无值」筛选暴露的类型。显式枚举（而不是委托给
 * isKnownPropertyType），让未来的属性类型必须主动选择是否支持筛选——它不该
 * 默认就可筛。
 */
export function isFilterablePropertyType(type: string): boolean {
  return (
    type === "select" ||
    type === "multi_select" ||
    type === "checkbox" ||
    isScalarPropertyType(type) ||
    isActorPropertyType(type)
  );
}

/** 单值标量属性类型：text / number / date / url。 */
export type ScalarIssuePropertyType = Extract<IssuePropertyType, "text" | "number" | "date" | "url">;

/** `type` 是否为单值标量属性类型。 */
export function isScalarPropertyType(type: string): type is ScalarIssuePropertyType {
  return type === "text" || type === "url" || type === "number" || type === "date";
}

/** 把 kind + id 拼成存储用的引用串，如 "member:<uuid>"。 */
export function formatActorRef(kind: IssuePropertyActorKind, id: string): string {
  return `${kind}:${id}`;
}

/** 解析一个 actor 引用串；不是合法引用时返回 null。 */
export function parseActorRef(raw: unknown): IssuePropertyActorRef | null {
  if (typeof raw !== "string") return null;
  const separator = raw.indexOf(":");
  if (separator <= 0) return null;
  const kind = raw.slice(0, separator);
  const id = raw.slice(separator + 1);
  if (!id) return null;
  if (!(ISSUE_PROPERTY_ACTOR_KINDS as string[]).includes(kind)) return null;
  return { kind: kind as IssuePropertyActorKind, id };
}

/**
 * 以存储顺序取出原始引用串，包括本构建不认识的 kind。
 *
 * 编辑器必须经此函数而非 `actorRefsFromValue` 做往返：否则连到更新后端的
 * 客户端会在用户切换某个它能理解的条目时，丢掉所有未知 kind 的条目——一次
 * 用户看不见的静默数据丢失。
 */
export function actorRefValuesFromValue(value: IssuePropertyValue | undefined): string[] {
  if (typeof value === "string") return value ? [value] : [];
  if (Array.isArray(value)) {
    return value.filter((entry): entry is string => typeof entry === "string");
  }
  return [];
}

/**
 * 把 actor 属性值读成一组「本构建能渲染」的引用。未知 kind 被丢弃而非抛错
 * ——见编辑路径用的 `actorRefValuesFromValue`，那条路径必须保留它们。
 */
export function actorRefsFromValue(value: IssuePropertyValue | undefined): IssuePropertyActorRef[] {
  if (typeof value === "string") {
    const ref = parseActorRef(value);
    return ref ? [ref] : [];
  }
  if (Array.isArray(value)) {
    return value.map(parseActorRef).filter((ref): ref is IssuePropertyActorRef => ref !== null);
  }
  return [];
}

/**
 * actor 值里是否至少含一条本构建无法解析的引用——即更新的后端放宽了
 * 接受的 kind。
 *
 * 编辑器在提供普通编辑前必须先查它。对 `multi_actor`，开关已能对未知条目
 * 做往返；但对单个 `actor`，无法解析的值渲染为空，让用户「补上这个空字段」
 * 会覆盖一条从未展示给他们的值（MUL-6286 review）。
 */
export function hasUnknownActorRef(value: IssuePropertyValue | undefined): boolean {
  return actorRefValuesFromValue(value).length !== actorRefsFromValue(value).length;
}

/** select/multi_select 属性的一个可选项。 */
export interface IssuePropertyOption {
  id: string;
  name: string;
  /** 规范化的小写十六进制颜色，如 `#3b82f6`。 */
  color: string;
}

/** 属性的类型相关配置（如选项列表）。 */
export interface IssuePropertyConfig {
  options?: IssuePropertyOption[];
}

/** 一条自定义属性定义。 */
export interface IssueProperty {
  id: string;
  workspace_id: string;
  name: string;
  /** 宽松字符串：更新的服务器可能携带本客户端不认识的新类型。 */
  type: string;
  description?: string;
  /** 可选目录图标键；不支持图标的旧后端上缺失。 */
  icon?: string;
  config: IssuePropertyConfig;
  position: number;
  archived: boolean;
  archived_at?: string | null;
  usage_count?: number;
  created_at: string;
  updated_at: string;
}

/** 一个属性值的存储形态：字符串 / 数字 / 布尔 / 字符串数组。 */
export type IssuePropertyValue = string | number | boolean | string[];
/** 议题的全部属性值：定义 id → 值。 */
export type IssuePropertyValues = Record<string, IssuePropertyValue>;

/**
 * 标量属性筛选的结构化操作符（#7692）。纯字符串成员仍表示精确相等——所有
 * 操作符出现前保存的视图、URL 与 API 调用语义不变。操作符以对象形式并列：
 *
 * - `contains` — 大小写不敏感的子串匹配（text / url）
 * - `gt` / `gte` / `lt` / `lte` — 数值比较（number）
 * - `before` / `after` — 字典序日期比较（date，"YYYY-MM-DD"）
 *
 * 同一属性定义内的成员仍是 OR 关系，因此一个操作符成员与已观察值、「无值」
 * 等任何替代项可组合。
 */
export type PropertyFilterOp =
  | "contains"
  | "gt"
  | "gte"
  | "lt"
  | "lte"
  | "before"
  | "after";

/** 全部已知的操作符。 */
export const PROPERTY_FILTER_OPS: readonly PropertyFilterOp[] = [
  "contains",
  "gt",
  "gte",
  "lt",
  "lte",
  "before",
  "after",
];

/** 标量筛选选择器与已激活 chips 共享的数学符号。 */
export const PROPERTY_FILTER_OP_SYMBOLS: Partial<Record<PropertyFilterOp, string>> = {
  gt: ">",
  gte: "≥",
  lt: "<",
  lte: "≤",
};

/** `op` 是否为已知操作符。 */
export function isKnownPropertyFilterOp(op: string): op is PropertyFilterOp {
  return (PROPERTY_FILTER_OPS as readonly string[]).includes(op);
}

/** 结构化操作符筛选成员。 */
export interface PropertyOperatorFilter {
  op: PropertyFilterOp;
  /** 用户原始输入；服务器按操作符解释。 */
  value: string;
}

/** 属性筛选的一个 OR 替代项：相等（裸字符串）、「无值」哨兵或结构化操作符。 */
export type PropertyFilterValue = string | PropertyOperatorFilter;

/**
 * 单个筛选成员的规范化成员键，让相等字符串与操作符对象能在 Set<string>
 * 形态的查找（保存视图基线、chip 增量）中共存，而不必临时序列化对象。
 */
export function propertyFilterValueKey(value: PropertyFilterValue): string {
  return typeof value === "string" ? value : `${value.op}\u0000${value.value}`;
}

/** `value` 是否为结构化操作符筛选成员。 */
export function isPropertyOperatorFilter(value: unknown): value is PropertyOperatorFilter {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as PropertyOperatorFilter).op === "string" &&
    typeof (value as PropertyOperatorFilter).value === "string"
  );
}

/**
 * 筛选菜单对每种标量属性类型提供的操作符，按展示顺序。列表里没有 "is"
 *（相等）——它是默认项并提交裸字符串，因此所有操作符前的流程保持不变。
 * 非标量条故意为空：保持记录穷尽，强制未来每种属性类型显式做出操作符决定。
 */
export const PROPERTY_FILTER_OPS_BY_TYPE: Record<IssuePropertyType, readonly PropertyFilterOp[]> = {
  text: ["contains"],
  number: ["gt", "gte", "lt", "lte"],
  select: [],
  multi_select: [],
  date: ["before", "after"],
  checkbox: [],
  url: ["contains"],
  actor: [],
  multi_actor: [],
};

/** 新建自定义属性的请求体。 */
export interface CreatePropertyRequest {
  name: string;
  type: IssuePropertyType;
  description?: string;
  icon?: string;
  config?: IssuePropertyConfig;
}

/** 更新自定义属性的请求体。 */
export interface UpdatePropertyRequest {
  name?: string;
  description?: string;
  /** 空字符串清除图标。 */
  icon?: string;
  config?: IssuePropertyConfig;
  archived?: boolean;
}

/** 自定义属性目录列表响应。 */
export interface ListPropertiesResponse {
  properties: IssueProperty[];
  total: number;
}

/** PUT/DELETE /api/issues/{id}/properties/{propertyId} 的响应：变更后的完整值袋。 */
export interface IssuePropertiesResponse {
  properties: IssuePropertyValues;
  issue_revision?: number;
}
