# 任务交付与固定环境发布

任务从确认计划、独立分支开发、固定 int 验证进入待审核；用户确认生产后，由现有外部发布器整合并发布。生产批准、代码提交、测试证据和实际运行版本分别保存，不能相互代替。

## 需求与开发

ida-developer、multica-developer 的沟通约定保存在各自 Agent 指令中：先查项目事实，澄清目标和边界，输出编号行动摘要及完整计划，用户确认一次后执行已授权动作。Multica 新任务从 `origin/personal-studio-upstream` 创建任务分支；已有任务续接原分支。保留其他任务的未提交内容。

原任务评论保存方案全文。登记评论ID及revision后，页面显示“确认计划并开始”；用户确认生成固定ID的真实评论，复用现有任务派发链。用户已在方案线程明确回复确认时，可引用该评论登记确认，不再问一次。方案修改会使原确认及未发布交付失效。

CLI 与页面共用接口：

```bash
multica issue delivery plan GONG-24 --file /absolute/plan.json
multica issue delivery confirm-plan GONG-24 --file /absolute/confirmation.json
multica issue delivery submit GONG-24 --file /absolute/delivery.json
multica issue delivery status GONG-24
multica issue delivery accept GONG-24 --delivery <UUID> --file /absolute/evidence.json
```

- plan：`comment_id`、`revision`。
- confirmation：`plan_id`、`revision`、`comment_id`；CLI引用当前方案线程里已存在的人类明确确认评论。
- delivery：固定 `operation_id`、完整 `source_commit`、`source_branch`、`base_commit` 和本地 `evidence`。证据是非空数组，每项含 `name`、`status: passed`、实际 `evidence`。重试保持操作号与内容一致。
- accept：当前交付 `revision` 和真实 int 业务验证的 `evidence`；初次本地证据不等于 int 验收。

## 排队与源码恢复

数据库的 `issue_delivery_plan`、`issue_delivery` 保存方案版本、固定提交、证据、用户批准和业务阶段。`.run/publish-requests/` 保存外部发布执行日志、结果及恢复资料，不能作为用户批准的替代来源。

外部 systemd 接力任务每15秒检查队列，不占 Agent 执行槽。正在验证的交付优先收尾，其余按创建顺序领取；int 被占用时保留排队状态，不 rerun、不重复 mention、不创建轮询自动化。接力单实例锁与现有 publisher/int/prod 锁共同串行化环境变更。

源码只从固定 Multica 仓库按完整提交恢复，建立 `refs/multica/deliveries/<UUID>`，在 `.run/delivery-sources/<UUID>` 创建干净工作区。原 Agent 任务目录不承担构建来源职责；运行结束可清理临时目录，分支和固定提交仍可续接。构建不携带 `.codebuddy`、`CODEBUDDY.md` 等运行时注入文件。

通过固定控制入口安装或恢复接力定时器：

```bash
pnpm --dir /data/ida/multica release delivery-worker --env int
pnpm --dir /data/ida/multica release delivery-worker --env prod
```

新运行产物携带同版本的发布操作脚本。发布入口验证服务后安装对应环境接力器；定时器是持久 systemd 单元，服务、发布器及机器重启后按数据库和日志继续。int 接力器禁止更新生产。配置、账号及环境地址取当前部署记录和私有 profile，不写入源码或日志。

## 联调与审核

联调源码必须是干净提交并包含已发布生产基线。复用固定 int，不建立额外预览；显式独立本地开发仍需要用户要求和 `MULTICA_LOCAL_DEV=1`。

接力器准备 int 后向原任务发送一次带交付编号、版本和入口的验收通知。负责人验证真实用户流程、关键组合、跨模块调用及持久化，提交简短测试清单和证据。接力器核对实际候选，执行原有 accept，释放 int，任务进入 `in_review`。

待审核代码留在任务分支，不提前合入主分支。其他任务可以使用 int；页面展示“已登记联调版本”，不把历史入口当作始终可访问的该任务版本。验证不足、冲突及运行异常有具体原因，不冒充通过。

## 用户确认与生产更新

页面“同意部署生产”是独立操作，确认对话框固定交付版本。任务身份不能自批；也可引用验收之后人类明确回复“同意部署 / 同意发布生产 / 确认部署生产”的评论。普通“继续”、`/note` 和 Agent 评论不构成生产批准。

批准后接力器整合最新 `origin/personal-studio-upstream`，重新准备并通知必要的 int 验证。没有实质改动时不重复索要发布批准；冲突或方案变化返回开发和审核。验证完成后快进合入、推送并核对远端主分支，再调用现有外部 publisher。主分支前进则重新排队整合和验证，禁止强推。

`release request` 与实际 publisher 都核对数据库中的批准、复核标记、原任务提交和候选身份。来源不符、批准失效或候选未包含批准提交时拒绝生产更新。

生产程序与 int 通过验收的程序相同，不重新编译。发布成功记录真实版本、日志及页面验收结果，任务仍等待用户最终确认完成；不自动设置 done。

## 短暂维护与恢复

停止服务前核对进程归属、环境身份、数据库连接、源码及运行文件摘要。自动迁移仅支持新增表和并发索引：先在旧站运行时应用，再分别准备旧、新版本 SQL 查询。其他结构变更必须有单独的兼容与恢复方案，默认拒绝自动发布，旧站继续服务。程序恢复不能撤销数据库变更。

发布器暂停领取新任务并等待已有任务正常结束，不强杀。默认等待900秒；超时恢复接单并报告，保留交付和恢复资料。实际切换时由临时维护进程接管 Web 端口，显示维护提示并自动探测恢复；允许短暂连接中断，不承诺零停机。

新版本通过页面资源、登录、API、WebSocket、环境隔离及致命日志检查后才算成功。启动或业务验收失败恢复切换前程序与配置；恢复失败保留占用和恢复资料，停止后续发布。不得清库、重置生产数据或反复自动发布失败版本。

## 诊断、清理与构建标识

`pnpm release status`、`verify`、`verify-logs --env int|prod` 查看实际状态。源码以 Git 为准；运行目录各保留当前版本，切换期间临时保留旧副本，成功后删除。数据库、上传文件及任务工作目录独立于程序目录。

成功后清理本次构建工作区，确认提交已进入主分支再清理临时分支，不删除其他任务成果。清理失败单独记录，不回滚已验收生产。显式备份使用 `pnpm release backup`，数据库删除另行授权。

页面“构建 MM.DD-HH:mm”读取当前运行产物的 `built_at`，使用北京时间。晋级生产、重启及恢复旧产物保留原时间；悬停显示完整日期与源码提交。页面发现运行版本变化时提示刷新，不能把当前部署时间当成重新构建时间。

## 运行代码差异

独立 Git 任务目录在启动 Agent 前、运行时配置注入后采集文件树；退出时在工作目录回收前再次采集。私有 Git index 不修改任务的 staging area、HEAD 或分支。两棵文件树的差异随完成回调保存到既有任务结果 `code_diff` 字段，使用任务本身的工作区权限，历史查看不读取当前仓库。

运行前已有的改动不进入该轮补丁；运行中提交的改动仍然包含。仅采集主工作目录仓库，不递归采集子模块或额外 checkout。普通共享目录和非仓库目录不展示归属不可靠的卡片；旧任务没有历史快照，也不回填当前 diff。隔离目录必须保持运行独占，外部同时写入该目录仍会进入文件树差异。

展示入口为任务执行记录、执行实录和运行复盘。无修改时隐藏卡片，二进制文件显示说明。最多展示 100 个文件、单文件 128 KiB 补丁、总计 1 MiB 补丁；超限明确标记部分结果。快照对象由 Git 对象库管理，卡片的补丁独立保存在任务结果中，不依赖对象或工作目录继续存在。此功能无需数据库迁移，也不会清理业务数据。
