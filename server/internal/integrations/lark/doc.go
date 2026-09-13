// Package lark 是 Multica ↔ 飞书（Lark）Bot 的接入包。
//
// MVP 范围见 MUL-2671。在相关迁移/服务边界 PR 落地后，本包目前覆盖：
//
//  1. DB schema 与 sqlc 封装（迁移 109_lark_integration.up.sql）
//  2. InstallationService（app_secret 加密存储，按 workspace 范围查询）
//  3. BindingTokenService（15 分钟一次性令牌，事务性兑换，在 DB 层拒绝跨用户重绑）
//  4. Feishu ResolverSet（feishu_resolvers.go），喂给共享的、与渠道无关的
//     engine.ChatSession（chat_session 的 ensure/append、/issue 解析）——
//     不再存在飞书专属的会话服务
//  5. Dispatcher（入站管线：安装路由 → 顶层 message_id 去重 → 群组过滤 →
//     身份校验 → ensure 会话 → append → /issue → 入队 chat task；对离线/归档/
//     指令引导输出类型化结果；emit 返回 DispatchResult + error，让 connector
//     能回贴对应的飞书侧回复卡片）
//  6. AuditLogger（lark_inbound_audit，刻意不设 body 列）
//  7. APIClient 接口 + http_client.go（真实飞书开放平台 IM v1 send/patch、绑定
//     引导、bot 信息的传输实现；未接入生产 client 时 stubAPIClient 拒绝一切调用）
//  8. Hub（WS 租约 + 每个安装一个 supervisor goroutine，带指数退避+抖动；租约
//     丢失时 renewer 会取消 connector 的 run ctx，从而保证多副本下 §4.4 的
//     所有权安全；EventConnector 接口是接真实线上协议的接缝）
//  9. WSLongConnConnector（基于 gorilla/websocket 的真实长连接；线上协议是官方
//     Go SDK 的二进制 Frame 信封——通过 POST /callback/ws/endpoint 引导建连、
//     应用层 ping/pong、每个数据帧回 ACK；ctx 取消时用看门狗 goroutine 打断
//     阻塞的 ReadMessage，以支撑 §4.4）
//  10. Patcher（订阅 task / chat-done 事件，保持每个 task 的飞书交互卡片同步；
//     节流 patch + 终态/错误直通）
//  11. OutcomeReplier（EventEmitter 契约的出站侧：NeedsBinding 铸造令牌并发绑定
//     引导；AgentOffline / AgentArchived 向群里推状态通知卡片；FreshPending /
//     IssueUsage 推指令引导；Ingested 由 Patcher 负责；Dropped 保持静默）
//  12. RegistrationService（RFC 8628 设备流扫码安装：对 accounts.feishu.cn 开
//     会话后台轮询，成功后走 InstallationService 落库，并经 InstallerBinder
//     自动绑定安装者，保证 §2.1「扫码即绑定即完成」端到端成立）
//
// 架构边界（Elon 二审冻结，MUL-2671 §4.8）：
//
//  1. 建 issue 必须走 internal/service.IssueService.Create —— 本包绝不直接调
//     qtx.CreateIssue。
//  2. 入站消息走共享的 engine.ChatSession，而非 HTTP 的 SendChatMessage
//     handler。群聊 chat_sessions 有多成员创建者语义，HTTP handler 的单一
//     创建者守卫是刻意拒绝这种语义的。
//  3. 出站卡片消息的映射放在 lark_outbound_card_message（按 task/message），
//     绝不落 chat_session.metadata。
//  4. 未绑定用户与非 workspace 成员永远到不了 chat_session/chat_message，
//     只会带 drop_reason 落入 lark_inbound_audit（无正文）且不再有其它痕迹。
//  5. app_secret 经 internal/util/secretbox 静态加密，DB 里绝不出现明文。
package lark
