/**
 * Runtime-path welcome modal starter tasks. These titles and prompts are
 * persisted into issues, so they live as code constants rather than i18n JSON.
 */

export const STARTER_CARD_IDS = ["intro", "tour", "welcome_page"] as const;
export type StarterCardId = (typeof STARTER_CARD_IDS)[number];

interface StarterPrompt {
  title: string;
  prompt: string;
}

export const HELPER_STARTER_PROMPTS: Record<StarterCardId, StarterPrompt> = {
  intro: {
    title: "简单介绍一下 Multica",
    prompt:
      "用 1-2 段话简单介绍 Multica 给我。讲清楚它是什么、核心概念有哪些(workspace / issue / agent / runtime)、和 Linear / Jira 之类的工具核心区别在哪。",
  },
  tour: {
    title: "带我熟悉每个功能",
    prompt:
      "陪我熟悉 Multica 的每个核心功能 —— issue、agent、squad、autopilot、chat。挑一个我可能用得上的真实场景,讲讲这几个东西是怎么配合的。",
  },
  welcome_page: {
    title: "用 slides 介绍 Multica 能为我做什么",
    prompt: `给我做一份单文件 HTML 演示稿,介绍 Multica 能为我做什么。根据我的角色和使用场景定制(见下面"关于我")。把完整 HTML 贴到这条 issue 的评论里的 \`\`\`html 代码块中,我直接复制下来存成 \`multica-intro.html\` 双击就能在浏览器里打开。

**产出格式**
- 一个自包含 .html,CSS / JS 全部 inline。零依赖、不用打包、不引外部图片(视觉用纯 CSS 生成 —— 渐变、几何形状、内联 SVG)。
- 5-8 张 slide:
  1. 标题页 —— "Multica 能为 [我的角色] 做什么"
  2. 四个核心概念 —— workspace / issue / agent / runtime,一张
  3-6. 3-4 个针对我使用场景的具体例子,形如"当你想做 X → Multica 是这样处理的"
  7. 收尾页 —— 一个具体的下一步动作

**视口约束(必须遵守)**
- 每个 \`.slide\`:\`height: 100vh; height: 100dvh; overflow: hidden;\`
- 所有 font-size 和 spacing 用 \`clamp(min, preferred, max)\`,不要写死 px / rem。
- 每张密度:1 个标题 + ≤ 4 个 bullet,或 1 个标题 + 2 段短段。超出就拆下一张。
- 兼容 \`prefers-reduced-motion: reduce\`(关动画)。

**审美(避免 AI 套路感)**
- 字体从 Fontshare 或 Google Fonts 选一个有辨识度的,不要用 Inter / Roboto / Arial / 系统字体。
- 用 CSS 变量统一调色板:一个主色 + 一个锐利的强调色。避免烂大街的"紫色渐变 + 白底"。
- 背景用层叠渐变或几何图案带氛围,不要纯白。
- 每张 slide 一次性的有节奏入场动画(用 \`animation-delay\` 错峰),CSS 实现。不要散落的微动效。

**导航**
- 左右方向键和空格切换,角落放一个小的页码指示。

做完后再用一句话告诉我你为我挑了哪几个场景以及为什么。`,
  },
};
