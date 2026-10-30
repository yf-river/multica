# OpenViking 平台接入

Multica 统一采集与召回；OpenViking 保存资料、会话、附件、记忆和经验。DSH、Codex 负责执行、压缩和原生计划／目标。知识库直接嵌入原生 Studio，使用现有 Multica 会话进入，不增加登录页面。

## 环境配置

生产 Server 与 Daemon 使用同一组配置；int、verify 和本地开发默认关闭。临时启用与隔离校验统一见 [环境发布说明](environment-releases.md#openviking-专项验收)，不把生产配置复制到测试环境：

```dotenv
MULTICA_ENV_ID=prod
MULTICA_OPENVIKING_ENABLED=true
MULTICA_OPENVIKING_URL=http://127.0.0.1:19446
MULTICA_OPENVIKING_API_KEY=<服务需要凭据时配置>
MULTICA_OPENVIKING_ARCHIVE_ROOT=<私有绝对路径>
MULTICA_OPENVIKING_CONFIG_FILE=<本机原生实例配置的绝对路径>
```

归档目录独立于可公开访问的 uploads，纳入备份。Server 保存附件原件和事务内事件；Daemon 在 `runs` 下落盘原始事件、关联附件和同步回执。同一环境使用一个归档写入 Daemon；不同环境使用不同数据库、OpenViking 数据目录、归档目录和运行时 profile。

OpenViking 采用 `default` account。本机个人部署使用原生 dev 模式，仅绑定 localhost；Studio 代理固定 account，直接浏览全平台数据。原件通过原生 content/batch-write 保存，不依赖账户管理接口。trusted 模式的数据面限制在当前 user，不能当作全平台浏览模式。凭据不下发浏览器。Agent 调用显式指定自己的 user 和召回范围。

原生文件系统显示 default account 下全部用户目录；会话、经验页按当前 user 展示，可在原生连接列表选择对应 Agent。该列表直接读取运行配置，不关联 Multica 登录用户。日常默认查看 multica，历史 default 仅供迁移核对。

Agent 的 `custom_env.MULTICA_OPENVIKING_USER` 可配置为 `ida`、`multica`。未配置时使用稳定的 `agent-<Agent ID>`，系统 Agent、审查员、质量监督、上游观察员和 Mika 同样适用，不合并到开发 Agent 的记忆中。禁止日常 Agent 配置为 `default` user。工作、生活共用共享资料，不根据工作区创建 OpenViking user。

DSH 使用随 Daemon 交付的 bridge，ready 必须声明原始归档能力。CodeBuddy 继续作为 DSH 模型插件。此链路不加载 DSH／Codex 的 OpenViking 记忆插件；检查实际生效 profile，避免另一套插件重复采集或召回。

经验提取需要原生配置 `server.agent_evolution.enabled=true`。这与普通用户记忆提取不同；仅目录存在、开关开启或 commit 返回成功，都不能证明产生了经验。

## 新会话的数据流

1. 用户消息、评论与附件的新增、更新、删除在原数据库事务中写入 `openviking_event`。事务回滚时采集也回滚。
2. Daemon 领取新任务前验证 OpenViking 可用及本地归档可写。不可用时任务留在队列，恢复后领取。已运行任务使用本地原始日志继续落盘。
3. 按 Agent user 召回共享资料及自己的记忆，再交给运行时。召回上下文有预算；归档原文不经过该预算或展示脱敏。
4. Codex 在界面过滤前采集原始协议；DSH 在根 Agent 过滤和工具展示裁剪前采集根／子 Agent 事件。用户追加输入、命令与交互答复也进入日志。日志逐条 fsync；持久化失败时取消执行并报错。
5. 附件保存实际字节、哈希和关联。处理 Multica 上传、DSH 原生对象及内联图片；原生工具引用的完整 stdout／stderr 日志和格式化输出在临时目录清理前同步复制，失败时保留受保护目录等待恢复；缺失原件保留失败状态，不能用文本链接代替完整附件。
6. 单一同步器上传原件并逐项回读哈希，把结构化用户／Agent 消息、工具输入与结果写入原生会话。原件独立于可读会话投影保存。
7. 平台来源等待相关任务结束再提交，使原生提取看到用户意图与执行结果。来源 ID 和原生归档回执用于重试核对；响应丢失时从原生历史恢复回执，不能盲目提交空会话。
8. 等原生记忆提取任务完成后记录同步完成，并确认平台事件交付。原生索引仍可能在后台处理，首次召回验收需等待索引完成。

当前完整采集支持 DSH、Codex。其它后端不能沿用展示消息冒充完整归档，会拒绝进入该链路。

同步失败显示在知识库底部；原始日志、附件和数据库事件均保留。进程中断后的半条日志保留原字节，投影仅消费完整前缀，并标记中断。OpenViking 原生终态提取任务没有公开重试接口；平台以原失败归档的全部消息创建确定性的 `multica-recovery-*` 会话，最多三轮重试。原会话不改写。仍失败时明确保留失败状态，不把它标为完成。

## Skill

共享 Skill 使用 `viking://agent/skills`，个人 Skill 使用 `viking://user/<user>/skills`。Daemon 通过原生文件接口取得完整目录，包含二进制附件和可执行脚本，并转换为现有运行环境所需文件。平台角色的内置输出契约继续由 Multica 提供。

历史 Skill 迁移保留全部原始文件和模式旁文件 `multica-skill.json`，原件位于共享迁移资料目录。原生导入可能规范化 Markdown、删除不支持的头部字段，或要求将 `sop.init` 规范化为 `sop-init`。运行时通过迁移旁文件定位原始主文档和原名称，避免丢掉 `user-invocable` 等执行元数据。原生目录中的正文和保留的元数据逐项校验；其它源文件必须字节一致。原生生成的目录摘要不作为运行时 Skill 附件。

Skill 缓存身份由内容、文件路径和模式计算，不依赖导出 ZIP 的时间戳。上游 OpenViking 更新时验证这些公开文件接口和 Skill 元数据；无需分别维护运行时记忆插件。

## 只读盘点与迁移

修改代码前先完成在线备份及独立恢复核对。在线文件复制与数据库快照不是跨系统同一时刻的快照；对复制窗口内变化的文件单独复核，不停生产写入。

以下操作必须使用实际连接配置确认环境，目录变量使用绝对路径。私有清单包含完整原文，不能提交到 Git：

```bash
go -C server run ./cmd/openviking-inventory --out "$INVENTORY_DIR" --native-root "$OPENVIKING_DATA_DIR" --upload-root "$UPLOAD_DIR"
python3 scripts/prepare-openviking-migration.py --inventory "$INVENTORY_DIR" --out "$PREPARED_DIR" --ownership "$OWNERSHIP_JSON"
go -C server run ./cmd/openviking-migrate --plan "$PREPARED_DIR/skills-plan.json"
```

`DATABASE_URL` 决定盘点源；原生迁移目标来自 `MULTICA_OPENVIKING_URL` 和服务端凭据。`--apply` 使用目标环境的完整配置：生产显式设置 `MULTICA_ENV_ID=prod` 与启用开关；测试目标必须通过专项验收隔离检查。`--out` 必须是新目录。盘点包括全部工作区、项目、Agent、事项、任务、聊天、评论、附件、Skill 文件及原生文件哈希。`changed_during_read` 非空时复核源文件。

归属文件明确指定原 Agent 与项目到 user 的映射：

```json
{"source_agent":"原 Agent ID","project_users":{"项目 ID":"ida","另一项目 ID":"multica"}}
```

准备命令生成 Skill 清单、未完成事项预览、历史保留项、未决事项和原生历史用户清单。没有项目或无法确认归属的记录保留原处。生产迁移前另行审阅目标及清单；本轮开发不执行生产数据切换。

隔离环境显式应用并回读对账：

```bash
go -C server run ./cmd/openviking-migrate --plan "$PREPARED_DIR/skills-plan.json" --apply
go -C server run ./cmd/openviking-agent-split --plan "$PREPARED_DIR/agent-plan.json" --targets "$TARGET_AGENTS_JSON" --expected-database "$EXPECTED_DATABASE"
```

Agent 目标文件为 `{"ida":"已有 Agent ID","multica":"另一个已有 Agent ID"}`。拆分命令默认只读；`--apply` 在一个事务中配置 user 并调整已核对的未完成事项。任何负责人、状态、版本或工作区与预览不一致都停止。历史事项、未决事项及其他负责人不改动；该命令不修改角色提示词或创建 Agent。

原生历史会话使用显式 `session` 清单迁移：`source` 为原件目录的绝对路径，`source_uri` 为原会话 URI，`user` 为确认的目标用户，`target` 为其新会话 URI，`hashes` 枚举全部相对路径及 SHA256。先用原生接口创建正确归属的目标会话，再保存完整源目录（包括包含全部摘要与身份元数据的标准 ZIP 和逐文件哈希清单）、重建可读消息及附件 URI；不把原身份元数据覆盖到目标会话。重复执行根据来源 ID 续传。历史会话不自动二次提炼记忆。

共享普通资料可用 `resource` 清单，目录原件与目标全部核对。历史 default、工作区用户及记忆中无法确认归属的数据保留原件和未决清单；不能按目录名称猜测删除或覆盖。Multica 历史展示记录曾经裁剪／脱敏的内容，只能从仍存在的运行时原始日志恢复；缺失来源必须作为缺口记录，数据库备份不能还原从未保存的原文。

## 验收与更新

使用独立 PostgreSQL 实例运行数据库 fixture 测试。固定 int 普通验收关闭 OpenViking；只有专项验收临时使用独立实例、数据、归档和本地存储，结束后恢复关闭，生产连接不用于 fixture。

验收覆盖：DSH／Codex 实际任务、原会话续跑与取消、计划／目标原生交互、长工具输出、子 Agent、图片与普通附件、消息编辑／删除／排队取消、附件删除后的原件、进程中断与重放、响应丢失、提取失败恢复、断服务暂停新任务及恢复后只执行一次、两个 Agent 和系统角色的记忆范围、完整 Skill 附件与脚本执行。

原生内容可读、异步提取完成、索引完成、后续真实任务使用记忆是四个验收点。批量导入时单独观察索引队列及模型服务容量，不以健康接口成功判断检索已经就绪。短期功能及恢复测试通过后，继续观察真实调用的失败、积压、耗时和资源曲线；不把一轮验收称为长期稳定性证明。

OpenViking 与 DSH／Codex 更新分别固定版本并运行上述受影响检查。官方 SDK／ACP 覆盖现有取消、审批、提问等必需行为之前，保留 DSH 必要桥接；不得以精简为由删除已使用功能。本功能的交付按用户约定直接构建 int、验收后 commit／push，不经过 Multica 发布流。
