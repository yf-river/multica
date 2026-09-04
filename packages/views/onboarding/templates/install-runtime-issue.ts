/**
 * Runtime guide created when a member skips runtime setup during onboarding.
 * The title remains stable so existing onboarding issues can be deduplicated.
 */
export const INSTALL_RUNTIME_ISSUE_TITLE = "连接运行时，和 Mika 开始";

export const INSTALL_RUNTIME_ISSUE_BODY = `欢迎来到 Multica。

智能体需要先连上运行时才能执行工作。运行时还没准备好时，你也可以先把 Multica 当作轻量项目管理工具体验起来。

## 先体验项目管理功能

运行时安装前，你可以先做这些事：

1. 为当前工作创建一个项目。
2. 新建几个任务，并在 backlog、todo、in_progress、done 之间流转。
3. 给任务加优先级、标签、评论和订阅。
4. 用收件箱追踪分配给你的事项和 @mention。

这样你先熟悉项目管理层。连上运行时后，智能体会直接在这些任务上开始工作。

## 安装第一个 Agent 运行时

完整文档：https://multica.ai/docs/zh/install-agent-runtime

中文用户建议先装 Kimi CLI：

1. 在 macOS / Linux 终端安装 Kimi CLI：
   curl -LsSf https://code.kimi.com/install.sh | bash
   Windows PowerShell：
   Invoke-RestMethod https://code.kimi.com/install.ps1 | Invoke-Expression
2. 确认终端能找到 Kimi：
   kimi --version
3. 在你想让 Kimi 工作的项目目录里启动一次：
   kimi
4. 首次启动后输入 /login，按提示完成 Kimi Code 或 API key 配置。
5. 等 Multica 识别到它。运行中的守护进程每隔几分钟会重新检查一次新装的 CLI，通常不需要重启。
   想立刻生效：
   multica daemon restart
6. 回到“运行时”页面刷新。你应该能看到一个在线的 Kimi 运行时。
7. 页面会显示“和 Mika 开始”；点击后会创建 Mika，并进入引导式的首次对话。

Kimi CLI 官方文档：https://moonshotai.github.io/kimi-cli/zh/guides/getting-started.html

Mika 会把一个真实目标转化为任务，交给合适的智能体启动执行，并在工作流需要时建议添加可复用的 specialist。`;
