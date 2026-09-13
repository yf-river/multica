// 「和 Mika 开始」引导入口的定义：目前只有会话标题，Mika 本体由服务端提供。

/** 引导入口数据：标题等前端展示信息。 */
export interface MikaOnboardingDefinition {
  title: string;
}

/**
 * Mika 的名字、描述、头像、权限和系统指令都不在这里——它们是服务端常量，
 * 由 `POST /api/agents/mika` 下发。不放进客户端，是为了让 Multica 可以只靠
 * 重新部署就更新 Mika 的提示词，也防止某个客户端自己铸造一个冒用 Mika 身份的
 * Agent。
 *
 * 只有会话标题留在客户端：它命名的是当前成员要打开的那一段会话。
 */
const MIKA_CHAT_TITLE = "和 Mika 开始";

/**
 * 返回「和 Mika 开始」入口的引导定义（供引导流程渲染按钮等）。
 */
export function getMikaOnboarding(): MikaOnboardingDefinition {
  return {
    title: MIKA_CHAT_TITLE,
  };
}
