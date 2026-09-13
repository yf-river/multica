/** 手动配置创建智能体路由：渲染共享的手动创建表单页。 */
import { ManualCreateAgentPage } from "@multica/views/agents/create/manual";

/**
 * 手动创建智能体。
 *
 * @returns 手动配置页。
 */
export default function NewAgentManualRoute() {
  return <ManualCreateAgentPage />;
}
