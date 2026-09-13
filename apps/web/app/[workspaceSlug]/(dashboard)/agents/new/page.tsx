/** 新建智能体入口路由：让用户选择创建方式（AI 对话搭建 / 手动配置）。 */
import { ChooseCreateMethodPage } from "@multica/views/agents/create/choose";

/**
 * 新建智能体（选方式）。
 *
 * @returns 创建方式选择页。
 */
export default function NewAgentRoute() {
  return <ChooseCreateMethodPage />;
}
