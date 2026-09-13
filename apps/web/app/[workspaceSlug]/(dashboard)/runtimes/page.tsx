/** 运行时列表页路由：把「是否启用云端运行时」的环境变量读好传给共享视图。 */
import { RuntimesPage } from "@multica/views/runtimes/runtimes-page";

// 云端（托管）运行时是否对当前部署开放，由构建期注入的环境变量控制；
// 服务端渲染时读一次，传给列表页决定是否展示「创建云端运行时」入口。
const cloudRuntimeEnabled =
  process.env.NEXT_PUBLIC_ENABLE_CLOUD_RUNTIME === "true";

/**
 * 运行时列表页。
 *
 * @returns 运行时列表视图。
 */
export default function RuntimesRoute() {
  return <RuntimesPage cloudRuntimeEnabled={cloudRuntimeEnabled} />;
}
