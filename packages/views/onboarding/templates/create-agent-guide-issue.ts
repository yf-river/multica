import { HELPER_DESCRIPTION, HELPER_INSTRUCTIONS } from "./helper-instructions";

const HELPER_AGENT_NAME = "Multica Helper";

export const CREATE_AGENT_GUIDE_ISSUE_TITLE = {
  zh: "第 2 步 —— 创建你的第一个 Multica Agent",
} as const;

interface BodyOpts {
  lang: "zh";
  installRuntimeIdentifier: string;
  installRuntimeId: string;
}

export function getCreateAgentGuideBody(opts: BodyOpts): string {
  const mention = `[${opts.installRuntimeIdentifier}](mention://issue/${opts.installRuntimeId})`;
  return zhBody(mention);
}

function zhBody(installRuntimeMention: string): string {
  return `等运行时上线（见 ${installRuntimeMention}）之后，把第一个 agent —— Multica Helper —— 建出来。下面的提示词已经写好，直接复制即可。

## 1. 打开新建 agent 页

在侧边栏点 **智能体** → 点 **新建智能体**。

## 2. 选你刚装好的运行时

在 "运行时" 下选它。如果什么都没有，说明运行时还没上线 —— 先按 ${installRuntimeMention} 把安装步骤走完。

## 3. 把下面三段分别复制到对应字段

**名称**
\`\`\`md
${HELPER_AGENT_NAME}
\`\`\`

**描述**
\`\`\`md
${HELPER_DESCRIPTION.zh}
\`\`\`

**指令**
\`\`\`md
${HELPER_INSTRUCTIONS.zh}
\`\`\`

## 4. 保存 → 分派 issue

点 **创建**。新 agent 会出现在 workspace 的 agent 列表里。

接着创建一个 issue（或把已有 issue 重新分派）→ 把 assignee 设成 Multica Helper → 状态切到 **todo**。运行时会在几秒内接走任务并开始工作。在 issue 的任务面板里看进度。

## 接下来去哪

- **技能** —— 可复用的指令包，可挂到任何 agent 上。
- **小队** —— 可一起被分派的一组 agent。
- **自动化** —— 定时或 webhook 触发的运行。
- **文档** —— https://multica.ai/docs。`;
}
