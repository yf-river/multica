G86 持续更新测试与 int-only 运维入口修复

本轮源码：dd94a2f8b6f329aa8ee39dddfd8740ed442d51ea，原任务分支 feature/ida-developer/gong-86。纯operations主线提交 5b57c4623；未发布G86生产业务。任务目标仍是准确候选固定int业务通过、正式in_review并释放占用，本报告不是完成声明。

修改与验证

1. 实际旧失败位于 TestIntPersistenceAndResume，并非已通过的 TestIntConcurrentDispatchAndLongHistory。保留 int51-final/fixed-int-five.log、REPORT.md及全部证据。本次使用无真实daemon的隔离在线CodeBuddy runtime，首次只使持续更新目标具备完成轮次资格，断言 completion 批次和 queued task 实际绑定；结束夹具批次后让第二条任务进入首审队列，断言显式重审先入队且竞争任务未入队。开发时竞争任务被选中的失败 test-first.log 及修复后通过的 test-second.log 均保留，未跳过或删除业务断言。
2. 定向模型入口仅额外接受 int + preparing_release + integration_only=true；所有既有scope、owner、candidate/source、seed、publication及selected idle Agent围栏保留。新建隔离PostgreSQL真实39项正反例通过；覆盖非本候选、生产、非目标Agent、忙碌、归属代次、无效/过期publication等，断言其他Agent和默认策略不变、未调用drain。临时容器 gong86-int-phase-pg 已删除。
3. prepare脚本按int-only真实阶段读取publication而非verify slot，并核对候选、实际环境/数据库、当前种子及候选validation_identity、归属和租约。候选delivery_env=prod表示控制端，不当作int环境身份。9项准备契约与6项入口测试通过。旧候选已released，实际prepare preflight被围栏拒绝，未应用配置；新候选的成功preflight/apply/cleanup留待部署后实测，不能声称已成功配置。
4. 完整operations主线与业务候选19文件逐字节一致；使用固定维护提交经原完整安装器更新int/prod，两端消费者和全部文件核对，指纹5bacd79e02c5fa64。未直接编辑已安装制品。ops-install.json、installed-int-audit.json可复核。
5. worker40项、release44项回归通过。开发源码对当前真实固定int执行全部五项TestInt，无跳过，均通过，见fixed-int-five-development.log；这不是尚未部署的新候选业务验收。

verify51复用边界

旧样本源码51cb16cab2dd68547ecc3d93ef9fc5bd707364b1，verify候选51cb16cab2dd68547ecc3d93ef9fc5bd707364b1-1792918336641334079，seed-20261025-agent-control-v4 / 5fcd7c40b1e78cf96ff8290d25718d6a9e463abbcc15c20a25859ba5371b6090。implementation-reuse.json逐文件记录3421个非测试server/迁移/packages/skills的旧新Git对象和SHA256，全部相同。新增实现变化仅测试和准备/operations入口，不改变监督服务、runtime提取、prompt/Skill或schema。

可精确复用verify51-final/REPORT.md中四组verification已证明范围：CodeBuddy首次3请求/同session续接2请求、取消/重试、已匹配的单个parent子请求、Codex所属Daemon零外部查询、历史混合runtime、分页/权限/重启正文不可变，以及六个真实监督批次的加载和效率对照。provider/model/session以runtime-and-session-proof.json及task.started原始消息为准；CodeBuddy实际deepseek-v4.1-flash-ioa，不能用int51中被默认策略覆盖的Codex样本冒充。外部partial、请求级缺口、模型正文阅读范围等原限制全部保留。这些均是旧版本真实样本，不是新候选新跑。

部署后接续

A. 新交付只串行submit，原交付01a1723e-ac21-78a7-a120-5842f6732045已由发布器正常转needs_changes revision9，publication idle generation83，旧证据未删除。
B. 核对准确候选实际提交/种子/生效配置；verification按逐文件覆盖映射复用旧样本，补本次入口实际配置并保存回执。不重复6批未变的真实监督模型。
C. 固定int准备：使用本候选prepare-supervision-acceptance.py，以实际runtime执行preflight、apply；定向Agent入口使用当前owner generation、publication generation、candidate/source、seed和控制策略revision，禁止手改phase或override。核对task.started.provider/model/session。
D. 必须对准确新候选重新执行五项TestInt、真实页面刷新持久化、审查回归及新环境组合；复用int51-final浏览器/审查配方但核对新前置、常量和元素。清理临时配置、归档本轮Agent并保留样本。
E. 正式accept真实integration证据，由原发布器完成int-only integrated、in_review并释放publication/candidate；无生产发布授权，不调用approve，不标done。

现有旧报告位置：/data/ida/artifacts/quality-token-supervision-20261025/int51-final/ 和 verify51-final/。本轮目录保留全部日志、SHA256映射及安装回执。

正式串行submit回执：新交付01a1725f-f94b-76a7-a04a-18fa0fb6fc84，已读取testing revision2，source_commit=dd94a2f8b6f329aa8ee39dddfd8740ed442d51ea。由原发布器准备固定候选，当前未登记最终integration通过或in_review，不以排队作为完成。
