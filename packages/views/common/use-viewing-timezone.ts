// 查看者时区 Hook：解析"用户当前该用哪个时区看时间"，优先级为用户偏好 → 浏览器
// 探测 → UTC。时区字符串本身（探测与偏好表）来自 ./timezone-select，本文件只负责
// 把它们串成一条决策链，并顺带过滤掉旧数据里的非法时区。

import { useAuthStore } from "@multica/core/auth";
import { browserTimezone } from "./timezone-select";

/**
 * 返回查看者当前应使用的 IANA 时区（如 "Asia/Shanghai"），优先级：
 * 1. 用户在偏好里保存的时区（auth store 中 user.timezone）；
 * 2. 浏览器探测到的本地时区；
 * 3. 都不可用时回退 "UTC"。
 *
 * 供所有需要"按查看者当地时钟展示时间"的地方使用——例如任务列表里把服务器给的
 * 时间戳分桶到"今天/昨天"时，必须用这个时区而不是硬编码某地时间。
 */
export function useViewingTimezone(): string {
  const stored = useAuthStore((s) => s.user?.timezone ?? null);
  if (stored && stored.trim() !== "") {
    try {
      // 用 Intl 尝试验证存的值是不是合法的 IANA 时区名；合法才采用。
      new Intl.DateTimeFormat(undefined, { timeZone: stored });
      return stored;
    } catch {
      // 旧数据可能存了并非合法 IANA 时区的标签。当成"未设置偏好"处理，
      // 这样后续按日期分桶的逻辑不会因非法时区抛异常把整页搞挂。
    }
  }
  return browserTimezone();
}
