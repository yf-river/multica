/**
 * Skip path, issue 1/2: connect a runtime before agents can execute work.
 */

export const INSTALL_RUNTIME_ISSUE_TITLE = {
  zh: "第 1 步 —— 连接运行时,开始使用 agent",
} as const;

const zh = `欢迎来到 Multica。

智能体需要先连上运行时才能执行工作。运行时还没准备好时,你也可以先把 Multica 当作轻量项目管理工具体验起来。

## 先体验项目管理功能

运行时安装前,你可以先做这些事:

1. 为当前工作创建一个项目。
2. 新建几个 issue,并在 backlog、todo、in_progress、done 之间流转。
3. 给 issue 加优先级、标签、评论和订阅。
4. 用收件箱追踪分配给你的事项和 @mention。

这样你先熟悉项目管理层。连上运行时后,智能体会直接在这些 issue 上开始工作。

## 安装第一个 Agent 运行时

完整文档:https://multica.ai/docs/install-agent-runtime

中文用户建议先装 Kimi CLI:

1. 在 macOS / Linux 终端安装 Kimi CLI:
   curl -LsSf https://code.kimi.com/install.sh | bash
   Windows PowerShell:
   Invoke-RestMethod https://code.kimi.com/install.ps1 | Invoke-Expression
2. 确认终端能找到 Kimi:
   kimi --version
3. 在你想让 Kimi 工作的项目目录里启动一次:
   kimi
4. 首次启动后输入 /login,按提示完成 Kimi Code 或 API key 配置。
5. 重启 Multica 守护进程:
   multica daemon restart
   如果你用桌面端,重启 app 即可。
6. 回到 Runtimes 页面刷新。你应该能看到一个在线的 Kimi 运行时。
7. 用这个运行时创建第一个智能体,再把一个 issue 分配给它,并把状态切到 todo。

Kimi CLI 官方文档:https://moonshotai.github.io/kimi-cli/zh/guides/getting-started.html

运行时连上后,你就可以创建 Multica Helper,开始一次有智能体参与的上手引导。`;

export const INSTALL_RUNTIME_ISSUE_BODY = { zh } as const;

export const FOLLOWUP_COMMENT_PREFIX = {
  zh: "完成后的下一步：",
} as const;
