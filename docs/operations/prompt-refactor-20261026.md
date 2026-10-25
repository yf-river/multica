# 2026-10-26 DSH/DeepSeek 提示词重构与监督预算收敛

## 结论

- DSH 任务的运行时 brief 从通用长模板切换为专用精简契约：同一 fixture 17,217 → **4,484 字节**；生产实际 `brief_bytes=5,497`（同机 codex 任务 43,836）。
- 监督材料预算 48k → 30k、单页 40k → 20k：生产监督单次 prompt 平均 **52,423 → 31,085 字符（-41%）**，最大 62,661 → 31,540。
- 新增每次任务的 `prompt composed` 日志，记录 `provider / brief_bytes / agent_instructions_bytes / prompt_bytes`，后续提示词改动可在真实任务上量化。
- DSH profile 显式设置压缩策略：`thresholdRatio=0.7`、`retainTokens=32768`、`maxTokens=16384`，已用真实 DeepSeek 调用验证可加载。
- 变更经交付队列发布：verify-2 业务验收 → 固定 int 集成 → 生产；生产实际运行 `f675aba8a`。

## 代码提交（origin/personal-studio-upstream）

| 提交 | 内容 |
|---|---|
| `688e49561` | DSH 专用 brief；prompt 体量日志；DSH 冒烟模型可由 `MULTICA_DSH_SMOKE_MODEL` 指定 |
| `d20af2ff2` | 监督材料预算 30k、单页 20k；预算回归测试 |
| `3d215a4ba` | `DSH_BRIEF_OUT` dump 辅助，便于真实运行时校验 brief |
| `34dd7ee10` | 删除 `server/internal/handler/file.go` 不可达响应（`go vet ./...` 阻塞项） |
| `f675aba8a` | 提交守卫：忽略 `.gong*-evidence/`、`.gong86-*/`、`.delivery-context.json*` |

## DSH brief 设计

只写 DSH 与模型无法推断的平台事实：

- 无人值守契约：不向用户提问、阻塞即停并发一条含选项与建议的评论、最多一次自修、证据要求、完成/失败边界。
- 不后台挂起、不 watch CI、不按可执行名杀 `multica`、临时文件放工作目录、stdout/stderr 不合并。
- 6 个核心 CLI、状态流转、5 步工作流、四段输出；若 Agent Identity 另有 JSON 契约则以其为准（监督/审查角色）。
- 仓库、项目资源、技能均为指针；长流程留在 `.dsh/skills` 按需加载。

调度仅在 `provider == "dsh"` 且 issue 任务时生效；chat、quick-create、Life 任务与其他运行时保持原路径。

## 真实运行证据

- 生产 daemon 日志：`provider=dsh brief_bytes=5497 agent_instructions_bytes=147 prompt_bytes=608`。
- 生产 DSH 冒烟 issue（`01a17438-4bfe-7697-9a8c-3a3cd2cd3ea7`，in_review）：任务 `completed`，模型读取 AGENTS.md 后回答 `in_review`，与契约一致。
- DSH 协议直连：`dsh --profile multica --stdio` + `deepseek-account/deepseek-flash`，读取 brief 后输出 `in_review`/`OK`，`status=completed`。
- 生产 `/health`：commit `f675aba8a9ed3402133449b1ca88f3ed59abb788`。

## 交付记录

- 交付任务：GONG-88；交付 `01a1742a-0827-722d-a409-5767e258571c`。
- 候选：`f675aba8a9ed3402133449b1ca88f3ed59abb788-1792950713798064352`。
- 验收：verify-2 通过后进入固定 int 集成，再由现有发布器切换生产；`issue_delivery.phase=published`。

## 角色指令现状（下一步）

生产现存 5 个 Agent，指令长度：review 2,127 字符（与代码默认已分叉）、supervision 8,637 字符（自定义）、mika 4,716、upstream 2,752、supervision Agent 本体 0。

收敛方向：把身份、授权、证据规则、输出 schema 拆成可组合片段；DB 只存渲染结果与自定义标记；设置页显示“是否等于默认”并支持重置；迁移生产自定义为受控版本后再删除被覆盖的默认文本。此项未在本次发布中改动生产 Agent 指令，避免在无人值守验收中同时改变审查行为。

## 后续测量

- 用 `prompt composed` 对比同一任务在 DSH 与 Codex 的固定前缀体量。
- 监督 prompt 体量继续按周采样；若下降后单次输出 token 未降，下一步压缩监督输出 schema 与 `MaxTurns`。
- 评估 OpenViking 仅限 int、长生命周期角色，指标见计划。

## 角色指令收敛（2026-10-26 复核）

- **交付审查员**：生产 `agent.instructions` 与代码默认 `server/internal/review/default_instructions.md` **逐字节一致**（5,332 字节），无漂移，无需改动。
- **质量监督**：生产自定义提示词 8,637 字符与代码默认相似度 97.7%，缺少 4 处平台规则（`round.scope` 语义、`round` 字段、`trace_usage` 材料类型、调查时刻与完成快照的区分）。已将生产配置切回默认：`instructions=""`，`instructions_version=default-31c7db8b`，`instructions_customized=false`；enabled、300 秒间隔、调查开关、运行时、模型 `gpt-6-luna`、thinking medium 均保持不变。原自定义文本备份在 `/tmp/sup-prod.txt`。
- **Mika / 上游观察员**：保留工作区自有指令（4,716 / 2,752 字节），不自动覆盖；如后续要收敛，需要逐段合并工作区特有内容后再清空自定义。

结论：审查与监督的提示词已回到单一来源；后续平台修正默认文本会自动生效，生产不再维护近似副本。
