 你是一名死代码清理执行者。仓库 /data/ida/multica 已删除 apps/desktop 与 onboarding UI 层，现在需要彻底清理 apps/ 与 packages/ 中残留的死代码、未用导出、i18n dead key 与死 package.json exports 条目。

  # 背景

  - 仓库：Go 后端 + monorepo 前端（pnpm workspaces + Turborepo）。
  - apps/web 是 Next.js 16 App Router。
  - packages/ 下有 core / views / ui / tsconfig / eslint-config。
  - apps/desktop 与 onboarding UI 层已删除，残留了对它们的引用、注释、导出与 i18n key。
  - 工程规范见 CLAUDE.md 与 AGENTS.md。代码注释用英文，对外文档用中文。
  - 依赖方向：views → core + ui；core 与 ui 互相独立；任何包不得导入 next/* 或应用专属代码。

  # 严格约束（违反即停止）

  - 禁止修改 server/ 目录（已清理过）。
  - 禁止修改 e2e/ 目录。
  - 禁止修改根目录配置文件（knip.json 除外，仅可删除 use-cases-source.ts 相关 ignoreFiles 条目；package.json / pnpm-workspace.yaml / turbo.json 等不动）。
  - 禁止新增任何文件、兼容层、回退路径或 shim。
  - 禁止改动 i18n 词汇表文案，只删 dead key。
  - 每一步删除前先用 grep / Glob 验证目标确实是死代码（零引用），再动手。
  - 路径以实际仓库状态为准：下面清单中的路径若与磁盘不符，用 find / grep 定位真实位置后再删，不要盲目按清单路径删除不存在的文件。

  # 最终目标（停止条件）

  满足以下全部条件即可停止：
  1. apps/ 与 packages/ 中无对 apps/desktop 的引用（注释、import、类型、字符串）。
  2. packages/ 中无死目录、死文件、死 barrel、死导出。
  3. packages/views/package.json 与 packages/core/package.json 的 exports 中无未用条目。
  4. packages/views/locales/ 下无 dead i18n key（代码零引用）。
  5. pnpm knip 无新增残留（与清理前基线相比只减不增）。
  6. pnpm typecheck 全部包通过。
  7. pnpm test 全部包通过。
  8. pnpm lint 通过。

  # 执行模型：分批 + 验证 + 回滚

  把下面 A–F 六批清理作为工作队列，每次只处理一批。每批流程：

  1. 预检：用 grep / Glob 核对这批涉及的所有路径与引用点确实存在且零引用。若某项已不存在或仍有引用且引用不在本批清理范围内，跳过该项并在日志记录原因，不要强行删除。
  2. 执行删除/修改：按清单逐项操作。删除整目录用 rm -rf，删文件用 rm，改文件用编辑工具。
  3. 同步清理：每项删除后，立即清理清单中标注的「同步清理点」（re-export、import、barrel、注释、defensive 调用等）。
  4. 验证：跑 pnpm typecheck && pnpm test && pnpm lint。若本批动了 server 相关文件（本任务不会），再跑 cd server && GOWORK=off go build ./... && go test ./...。
  5. 判定：
     - 验证全绿 → 该批成功，保留改动，输出 diff 摘要与验证结果，进入下一批。
     - 验证失败 → git restore 回滚本批改动（仅回滚本批触及的文件），记录失败原因与回滚范围，继续下一批（不要卡住重试同一批）。
  6. 批间 knip：每完成 2 批或全部批次结束前，跑一次 pnpm knip 确认无新残留。

  # 删除清单

  ## 批次 A：apps/web/ 死代码

  A1. 删除 apps/web/app/custom.css，并删除 apps/web/app/globals.css:6 的 @import "./custom.css"; 行。
  A2. 删除 apps/web/app/layout.tsx 中 Source_Serif_4 字体加载：移除 import 中的 Source_Serif_4、sourceSerif 常量定义、className 中的 sourceSerif.variable。若移除后该字体相关变量无其他引用，一并清掉。
  A3. 删除 use-cases 内容源：
     - 删 apps/web/lib/use-cases-source.ts
     - 删 apps/web/content/use-cases/ 整目录
     - 删 apps/web/source.config.ts 中 use-case schema（useCaseFrontmatterSchema、useCases defineDocs、相关 import 与注释）。若删后 source.config.ts 只剩 defineConfig 默认导出，保留它；若整个文件不再被引用，按 knip
  结果决定是否删。
     - 删 knip.json 中 ignoreFiles 数组里的 "apps/web/lib/use-cases-source.ts" 与 "apps/web/source.config.ts" 两条（仅这两条，其他条目不动）。
  A4. 删除 apps/web/app/(auth)/onboarding/page.tsx（重定向 shim）。删前确认该路由无其他引用。
  A5. 清理 4 处指向已删 apps/desktop 的陈旧注释（只删注释行/改写注释，不删功能代码）：
     - apps/web/app/layout.tsx 第 19-20 行附近的 desktop 同步注释
     - apps/web/app/globals.css 第 24 行附近的 apps/desktop/src/renderer/src/globals.css. 注释
     - apps/web/components/web-notification-bridge.tsx 第 13-14 行附近的 desktop counterpart 注释
     - apps/web/app/[workspaceSlug]/(dashboard)/agents/page.tsx 第 3-9 行附近的 DesktopAgentsPage 注释（清单原写 agents/page.tsx:6-7，真实路径在 [workspaceSlug]/(dashboard)/agents/ 下，用 grep 定位 DesktopAgentsPage / desktop
  确认）

  ## 批次 B：packages/views/ 死目录与死文件

  B1. 删 packages/views/dashboard/ 整目录（含 index.ts、utils.ts、utils.test.ts、components/dashboard-page.tsx、components/dashboard-page.test.tsx）。删前 grep 确认 DashboardPage 与 dashboard barrel 零引用。
  B2. 删 onboarding 残留：
     - 删 packages/views/onboarding/templates/ 整目录（含 index.test.ts、user-context.test.ts 等）
     - 删 packages/views/workspace/welcome-after-onboarding.tsx 与 packages/views/workspace/welcome-after-onboarding.test.tsx
     - 删 packages/views/locales/zh-Hans/onboarding.json
     - 同步清理 packages/views/locales/index.ts：删 import zhHansOnboarding 与 resources 对象中的 onboarding: zhHansOnboarding 行
     - 同步清理 packages/views/i18n/resources-types.ts：删 onboarding 的 import 与 type 声明
     - 删 packages/core/onboarding/welcome-store.ts
     - 同步删 apps/web/components/web-providers.tsx 第 69-75 行附近的 useWelcomeStore.getState().reset() 调用及其注释（删前确认 useWelcomeStore import 也一并移除，若该 import 是文件内唯一引用）
  B3. 删 packages/views/modals/create-issue.tsx 与 packages/views/modals/create-issue.test.tsx。删前确认 registry 实际用的是 create-issue-dialog 而非此文件。
  B4. 删 packages/views/layout/workspace-presence-prefetch.tsx 与 packages/views/layout/workspace-loader.tsx。同步清理 packages/views/layout/index.ts 中对这两个文件的 re-export。
  B5. 删 packages/views/issues/hooks/index.ts（死 barrel）。删前确认无 import 该 barrel 的代码。

  ## 批次 C：packages/ui/ 与 packages/core/ 死代码

  C1. 删 packages/ui/markdown/StreamingMarkdown.tsx。同步清理 packages/ui/markdown/index.ts 的导出：移除
  StreamingMarkdown、StreamingMarkdownProps、MemoizedMarkdown、CodeBlock、InlineCode、CodeBlockProps、detectLinks、hasLinks、isCdnUrl、isFileCardUrl 这些条目（删前对每个导出名 grep
  确认零外部引用；若有引用，保留该导出并在日志记录原因）。
  C2. 删 packages/ui/hooks/use-auto-scroll.ts。删前 grep 确认零引用。
  C3. 删 packages/core/markdown/ 整目录。同步更新 packages/ui/markdown/mentions.ts 第 9-13 行的 SYNCED COPY 注释，移除对 packages/core/markdown/mention-shortcodes.ts 的引用说明（因为 core 副本已删，ui 副本成为唯一来源）。
  C4. 简化 packages/core/analytics/index.ts 的 detectClientType()：把函数体改为恒返回 "web"，移除 window.electron / desktopAPI 探测分支；把 ClientType 类型从 "desktop" | "web" 简化为
  "web"（若简化类型会破坏其他签名，则保留联合类型但函数恒返回 web）。同步删 packages/core/analytics/index.test.ts 第 68 行附近的 detects desktop when window.electron is present 测试用例。
  C5. 简化 packages/views/platform/open-external.ts 第 18-26 行：改为直接 window.open(url, "_blank", "noopener,noreferrer")，保留 SSR guard（if (typeof window === "undefined") return;），移除 desktop 分支。
  C6. 清理 packages/core/platform/system-notification.ts 第 1-16 行与第 42-43 行的 desktop 注释（只删注释，保留功能）。

  ## 批次 D：死 package.json exports 条目

  D1. packages/views/package.json 的 exports 中删除以下条目（删前对每个 entry 用 grep 确认零外部 import；若有 import，先确认该 import 是否属于本任务其他批次正在删除的代码，若是则随该批一起删，否则保留并在日志记录）：
     ./onboarding、./i18n、./common/actor-avatar、./common/task-transcript、./common/markdown、./issues/utils/filter、./issues/utils/sort、./modals/registry、./modals/create-issue、./workspace/workspace-avatar、./workspace/creat
  e-workspace-form、./settings/lark-tab、./platform

  D2. packages/core/package.json 的 exports 中删除以下条目（同样删前 grep 验证）：
     ./markdown、./issues/config/status、./issues/config/priority、./agents/derive-presence、./agents/use-agent-presence、./agents/scope-label、./i18n/browser

  注意：./onboarding、./i18n 等 exports 可能与批次 B 的文件删除耦合，建议批次 B 成功后再做 D，或在 D 中遇到仍被引用的 entry 时跳过并等 B 完成后重试。

  ## 批次 E：i18n dead key

  在 packages/views/locales/zh-Hans/ 与其他 locale 目录（若存在同名 key）中删除以下 dead key。删前对每个 key 名用 grep 全仓搜索确认零引用（搜索范围含 .ts/.tsx，排除 .json 与 locales 目录本身）。若某 key
  在代码中仍有引用，跳过并记录。

  E1. agents.json 的 activity_tooltip 对象（约第 93 行，含 6 个子 key：created_today、created_days_ago_other、last_7_days、no_activity、runs_other、failed_suffix）
  E2. agents.json 的 task_failure 对象（约第 465 行，含 5 个子 key：agent_error、timeout、runtime_offline、runtime_recovery、manual）
  E3. runtimes.json 的 running_other（约第 155 行）
  E4. runtimes.json 的 queued_other（约第 156 行）
  E5. runtimes.json 的 cli_managed_badge（约第 209 行，值为 "桌面端"）

  注意：若存在 en/ 或其他 locale 的同名文件同名 key，一并删除以保持 locale 对齐。

  ## 批次 F：孤立测试文件

  删除因批次 A–E 删除源码而残留的孤立测试文件。这些测试文件已在对应批次中列出，确保它们随源码一起删除：
  - packages/views/dashboard/components/dashboard-page.test.tsx（随 B1）
  - packages/views/dashboard/utils.test.ts（随 B1）
  - packages/views/workspace/welcome-after-onboarding.test.tsx（随 B2）
  - packages/views/onboarding/templates/index.test.ts（随 B2）
  - packages/views/onboarding/templates/user-context.test.ts（随 B2）
  - packages/views/modals/create-issue.test.tsx（随 B3）

  若批次 A–E 已把这些测试文件随源码删除，本批次无需重复操作，直接确认即可。

  # 验证命令

  每批清理后（按顺序）：
  1. pnpm typecheck（覆盖全部 5 个包）
  2. pnpm test（覆盖全部 7 个包，Vitest）
  3. pnpm lint
  4. 每 2 批或全部结束前：pnpm knip（死代码扫描，确认无新残留）

  若本批改动涉及 server（本任务不应发生）：cd server && GOWORK=off go build ./... && go test ./...

  # 失败处理

  - 任一验证命令失败 → git restore <本批触及的文件列表> 回滚本批 → 记录失败批次、失败命令、错误摘要、回滚文件列表 → 继续下一批。
  - 不要在同一批上反复重试超过 1 次。第一次失败回滚后，可尝试修正一次明显的连带引用（如漏删的 import），修正后重跑验证；再失败则永久回滚该批并跳过。
  - 跳过的批次在最终报告中标为「已跳过 + 原因」。

  # 输出要求

  每批结束后输出一段批日志，格式：

  ## 批次 <字母> — <成功|回滚|跳过>
  - 操作项：A1 删 custom.css / A2 清 Source_Serif_4 / ...
  - diff 摘要：删 N 文件、改 M 文件，关键改动 <一句话>
  - 验证：typecheck PASS | test PASS (X passed) | lint PASS | knip <无新残留/新增 N 条>
  - 若回滚：回滚原因 <错误摘要>，回滚文件 <列表>
  - 若跳过：跳过项 <X>，原因 <零引用未确认/路径不存在/...>

  全部批次结束后输出最终总结：
  - 各批次结果汇总表
  - 最终 pnpm knip 输出（确认无新增残留）
  - 最终 pnpm typecheck && pnpm test && pnpm lint 结果
  - 是否达成停止条件；未达成的项与建议下一步

  # 停止条件（独立校验模型据此判定 goal 是否完成）

  以下全部为真时 goal 完成：
  1. 批次 A–F 全部处理完毕（成功、回滚后跳过、或确认无操作）。
  2. pnpm typecheck 全绿。
  3. pnpm test 全绿。
  4. pnpm lint 全绿。
  5. pnpm knip 无新增残留（相对清理前基线只减不增）。
  6. grep 确认以下字符串在 apps/ 与 packages/ 的 .ts/.tsx/.css 中零命中：apps/desktop、Source_Serif_4、DesktopAgentsPage、useWelcomeStore、StreamingMarkdown、activity_tooltip、task_failure、cli_managed_badge。
  7. packages/views/package.json 与 packages/core/package.json 的 exports 中清单 D 列出的条目均已删除（或已记录「仍有引用、保留」的跳过原因）。

  若第 6 项有命中，检查命中是否属于「本应保留的引用」；若属于遗漏未删，补删后再验证。若连续 2 轮无任何可删项且仍有命中，把命中清单写入最终报告并停止。