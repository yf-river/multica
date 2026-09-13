// Runtime 的派生"健康"类型——在列表、卡片与提示里展示的用户可见状态。
// 原始服务器字段是二元的（online / offline + last_seen_at）；这个枚举把离线态
// 拆成三个按时间分桶的口味，让用户能区分"刚断"和"很久没见"。

/** Runtime 的四态健康：在线 / 刚失去 / 离线 / 长期离线。 */
export type RuntimeHealth =
  | "online" // 绿——心跳阈值内
  | "recently_lost" // 琥珀——离线 < 5 分钟（多半是暂时的）
  | "offline" // 灰——离线 5 分钟 ~ 6 天
  | "long_offline"; // 红——离线超过 6 天
