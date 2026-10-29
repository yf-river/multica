# Multica DSH Runtime

DSH 的 Multica profile bundle。DSH 负责 session、模型、工具和上下文，bridge 将任务和事件转换为 JSONL v1；stdout 只用于协议，诊断写入 stderr。

## 构建与验证

使用 Node `^22.19.0 || >=24` 和仓库 pnpm：

```sh
pnpm --filter @multica-ai/dsh-runtime check
pnpm --filter @multica-ai/dsh-runtime pack:runtime
pnpm --filter @multica-ai/dsh-runtime bundle:daemon
```

`artifacts/dsh-runtime-<version>.tgz` 是安装制品；`manifest.json` 记录版本、兼容范围、大小和 SHA-256。`dist` 由源码构建，不提交。Go daemon 内置的 tarball 和清单位于 `server/internal/daemon/dshbundle/bundle`，由源码生成并提交，CI 核对一致性。`fixtures` 由 Go adapter 与 bridge 测试共同读取。

包名和 profile 入口为 `@multica-ai/dsh-runtime` 与 `multica`：

```sh
dsh plugin --profile multica add /absolute/path/to/dsh-runtime-0.3.4.tgz
dsh --profile multica --probe
dsh --profile multica --list-models
dsh --profile multica --stdio
```

安装或 smoke 使用独立测试 profile；共享或生产 profile 通过 daemon 的受控供给流程维护。`compat.json` 声明支持范围，交付证据另记精确验证过的 DSH 版本；DSH 安装不锁定精确版本。

## 运行契约

- JSONL v1 支持续跑、取消、模型发现、工具、用量、context/compaction 和 MCP。
- `MULTICA_DSH_SESSION_ROOT` 将日志隔离到 runtime/Agent；用户 profile 和凭据由 DSH 管理。
- 只向子工具转发服务器签发的 `mat_` 任务令牌，不转发模型凭据。
- `MULTICA_DSH_INTERACTIONS=1` 在 daemon 启用持久化计划评审和普通问题；默认关闭。
- 开关启用后，`exit_plan_mode` / `ask_user_question` 输出 `plan_review` / `user_question`，当前原生工具取消并完成会话持久化，进程以 `waiting_plan` / `waiting_input` 结束。
- 人工决定创建下一轮任务。批准通过原生 `/plan off` 恢复并执行；继续规划通过 `/plan` 恢复；普通回答作为下一轮输入。原生会话不可恢复时停止，不能用新会话代替批准执行。
- DSH 会话标题的模型生成关闭；聊天标题由 Multica 维护。模型历史、工具结果、context/compaction 和缓存计量由 DSH 提供。
- `approval_policy=never` 是现有角色只读协议，正式验收和显式 Agent 设置另行交付。

凭据、私有 session、生成的 profile 和真实任务输出不进入本包。

## 无付费模型的原生交互验收

此入口启动真实 DSH 引擎，使用确定性本地 provider，在新建的无凭据 `DSH_HOME` 中验证计划、普通问答、进程退出、重启续跑和 bash 写入。它验证原生执行链，模型规划质量和真实缓存命中需要另行验证。

```sh
MULTICA_RUN_REAL_AGENT_SMOKE=1 python3 packages/dsh-runtime/scripts/test-native-interactions.py \
  --dsh /absolute/dsh \
  --host-package /absolute/dsh/package \
  --bundle /absolute/dsh-runtime-0.3.4.tgz \
  --artifacts /private/evidence
```

每次运行创建独立证据目录；保留前轮输出。`tests/fixtures/native-provider.mjs` 只用于这个显式入口，不能安装到日常 profile。
