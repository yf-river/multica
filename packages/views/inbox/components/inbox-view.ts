// 收件箱两种列表视图的类型与 URL 参数常量。

/**
 * 收件箱当前展示的是两个列表里的哪一个。
 *
 * "inbox" 是默认的活跃通知列表；"archived" 是从该列表底部入口进入的子视图。
 * 两者对同一条 issue 互斥——服务端决定某条 issue 属于哪个列表（见
 * ListArchivedInboxItems）——所以「视图」是整组数据源与行操作的一次切换，
 * 而不是在一个列表之上叠加的筛选。
 *
 * 持久化在 URL 的 `?view=archived` 里，这样刷新、前进/后退一步或移动端从详情
 * 返回，都会回到用户本来所在的列表。
 */
export type InboxView = "inbox" | "archived";

/** URL 里表示「归档视图」的查询参数值。 */
export const ARCHIVED_VIEW_PARAM = "archived";
