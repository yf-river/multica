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
dsh plugin --profile multica add /absolute/path/to/dsh-runtime-0.3.0.tgz
dsh --profile multica --probe
dsh --profile multica --list-models
dsh --profile multica --stdio
```

安装或 smoke 使用独立测试 profile；共享或生产 profile 通过 daemon 的受控供给流程维护。`compat.json` 声明支持范围，交付证据另记精确验证过的 DSH 版本；DSH 安装不锁定精确版本。

## 运行契约

- JSONL v1 支持续跑、取消、模型发现、工具、用量、context/compaction 和 MCP。
- `MULTICA_DSH_SESSION_ROOT` 将日志隔离到 runtime/Agent；用户 profile 和凭据由 DSH 管理。
- 只向子工具转发服务器签发的 `mat_` 任务令牌，不转发模型凭据。
- 当前计划评审仍沿用自动批准；持久化交互在后续独立交付中接入公共交互服务。
- `approval_policy=never` 是现有角色只读协议，正式验收和显式 Agent 设置另行交付。

凭据、私有 session、生成的 profile 和真实任务输出不进入本包。
