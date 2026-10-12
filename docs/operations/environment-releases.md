# 联调与生产发布

同一份发布包先在联调验证，再发布生产。环境配置、数据库、账号、上传文件、Daemon profile 和 CodeBuddy 会话留在各自环境中。

## 日常操作

在仓库根目录运行：

```bash
node scripts/goal-test-environments.mjs build
node scripts/goal-test-environments.mjs deploy int <release-id>
# 完成同浏览器登录、API/WS、上传和真实 Agent 任务验收后：
node scripts/goal-test-environments.mjs deploy prod <同一个release-id>
node scripts/goal-test-environments.mjs verify
```

`build` 复制包括已跟踪修改及未忽略新文件的源码快照；快照期间源码发生变化会失败。版本由 Git SHA 和源码内容摘要共同标识。环境文件不进入发布包。Go 程序与 Next standalone 在独立构建目录生成，不使用服务正在运行的构建目录。

`deploy` 校验发布包文件摘要、隔离配置和进程归属，暂停 Daemon 新认领，等待任务完成，再切换 API、Web 与 Daemon。相同环境同时只能运行一个发布操作；失败会尝试恢复切换前版本。生产接受联调当前或上一版本、当前生产版本或生产上一版本；发布前仍须完成对应版本的业务验收。

```bash
node scripts/goal-test-environments.mjs rollback int
node scripts/goal-test-environments.mjs rollback prod
```

回滚只切换程序，不重置配置和数据库。至少安装两个隔离版本后才有上一发布包可回滚。旧版接管基线另存于生产 baseline 目录，不能作为常规隔离版本回滚。本次没有数据库迁移；以后包含迁移的发布必须先审核其向前、向后兼容性，迁移程序包含在包内但发布命令不会自动运行迁移。

## 配置与文件归属

| 路径 | 内容 |
|---|---|
| `.run/artifacts/<release-id>/` | 发布包与 `release.json` 文件摘要；不在此启动服务 |
| `.run/releases/<int或prod>/<release-id>/` | 各自安装的程序、静态资源和 Next 缓存 |
| `.run/env/<int或prod>-services.json` | Server、Web、Daemon 的私有运行配置真源，权限 0600 |
| `.run/env/goal-test-<int或prod>.env` | 为旧审计工具生成的配置副本，不手工编辑 |
| `.run/state/<int或prod>/` | 独立的 CodeBuddy 配置、会话和各进程临时目录 |
| `.run/uploads/goal-test-<int或prod>/` | 本环境上传文件 |
| `.run/workspaces/goal-test-<int或prod>/` | 本环境任务工作副本及允许原地写入的资源根 |
| `~/.multica/profiles/goal-test-<int或prod>/` | Daemon 身份、现有 PAT、工作区绑定及日志 |
| `.run/deployments/goal-test-<int或prod>.json` | 当前实际版本、安装目录与进程记录 |

`initialize int` / `initialize prod` 仅用于一次性接管已运行的旧环境，导入其实际配置并生成独立 JWT 密钥；环境已初始化时会拒绝覆盖。普通发布不调用账号初始化、演示数据填充或凭证重新绑定。

`MULTICA_ENV_ID` 在同一环境内长期固定，Web 与 Server 必须一致。所有应用 Cookie 按该标识命名，包括身份、CSRF、登录标记、最近工作区和注册来源。第一次迁移需重新登录一次；普通升级不更换 JWT 密钥。未指定标识的其他自托管实例继续使用原 Cookie 名称。

`MULTICA_ENV_WRITABLE_ROOT` 限制原地任务的写入目录；指向外部仓库的资源需选择 worktree 模式。路径校验解析符号链接。它是任务执行路径规则，不是操作系统沙箱。

CodeBuddy 使用相同个人账号，各环境分别复制初始配置并通过 `CODEBUDDY_CONFIG_DIR` 运行。后续授权更新在相应环境配置中维护，发布不覆盖它们。个人默认配置目录不被发布过程修改。机器资源和账号配额仍然共用。

## 验收

- 设置 `MULTICA_TEST_ACCOUNT`、`MULTICA_TEST_PASSWORD` 后运行 `node scripts/verify-release-browser.mjs`；在一个浏览器上下文中同时登录两边，交替刷新、退出与重新登录；另一边会话应继续可用。
- 验证正确环境的 CSRF 写请求与 WebSocket，跨环境 JWT 必须拒绝；最近工作区跳转只能使用本环境 Cookie。
- 上传后下载核对内容；新建真实 CodeBuddy 小任务并检查终态、工作目录、产物和回调，不能以 runtime 在线代替执行成功。
- 构建、发布及回滚联调期间持续探测生产登录、页面和静态文件。
- 重复发布同版本，比较账号、工作区、密钥和 Daemon 绑定；只清理验收创建的数据。
- `verify` 核对实际进程配置、路径、端口、API 身份和 Daemon 绑定，输出中不包含密钥。

联调和生产当前端口分别为 Web 13682 / 13680、API 18762 / 18760；具体运行配置以私有配置文件为准。生产与联调数据库互相独立，不随发布复制。
