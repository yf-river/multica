// Package engine 是渠道无关的运行时——它**驱动**父包 channel 里定义的 channel.Channel
// 适配器。第一阶段（MUL-3515）交付了抽象（Channel / InboundMessage / OutboundMessage /
// Registry）但没有引擎消费它；本包就是那个引擎——MUL-3620 的"通用引擎"，从飞书专属的
// lark.Hub / lark.Dispatcher 泛化而来。
//
// 它提供：
//
//  1. Supervisor —— 从 lark.Hub 泛化出的"每个安装一条连接"监督器。它枚举**所有**渠道类型
//     的活跃安装（不硬编码平台），把每个安装围在 WS 租约 CAS 后面（跨副本每个安装至多
//     一个副本在连接），经 channel.Registry 构建平台 Channel，以指数退避 + 抖动驱动它的
//     Connect/Disconnect 生命周期，并在凭据轮换后重启对应连接。
//
//  2. Router —— 从 lark.Dispatcher 泛化出的入站流水线。它是 Supervisor 注入每条 Channel
//     的、那个唯一共享的 channel.InboundHandler：按 ChannelType 路由到该平台注册的
//     ResolverSet，并对每个平台跑同一条有序流水线（安装路由 → 两阶段去重 → 群聊 @bot
//     过滤 → 身份 + 成员资格 → 保证会话 → 追加+标记 → /issue → 去抖运行），随后驱动
//     解耦的 OutboundReplier 与打字指示器。
//
// 一切平台特有的东西都藏在 resolvers.go 的 resolver 接口后面；新增平台就是"注册一个
// Factory + ResolverSet"，绝不是"改引擎"。引擎只依赖 channel 包、平台无关的服务层和
// 小接口；DB-backed 的 store/resolver 与具体平台适配器在启动时接线。
package engine
