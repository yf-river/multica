// Package channel 是 Multica 各入站 IM 集成（飞书/Lark、Slack、企微…）共享的"平台无关
// 地基"。它拥有每个集成都要实现的那份契约，核心因此永远不需要知道某个平台的事件 JSON
// 长什么样——设计见 MUL-3506，第一阶段地基见 MUL-3515。
//
// 契约由四块组成：
//
//  1. Channel —— 每集成一实现的那个接口
//     （Type / Connect / Disconnect / Send / Capabilities）。适配器双向翻译平台载荷、
//     自持连接形态（出站 WebSocket 长连接、入站 HTTP…）；核心只调用这五个方法。
//
//  2. InboundMessage / OutboundMessage —— 规范化消息信封。每个平台收到的入站载荷都由
//     其适配器翻译成一条 InboundMessage；核心只对这个结构体做路由、去重与持久化。
//     Outbound 是最小文本回复（ChatID + Text + 可选的线程/回复目标）——富卡片、媒体、
//     出站 webhook 刻意不在本包范围内，留在支持它们的适配器里。
//
//  3. Capability —— 每个 Channel 用来**声明**自己能力（富卡片、线程、附件…）的位掩码。
//     本包只建模"声明"，刻意不含降级逻辑。想降级的调用方（富卡片 → 纯文本）自己读
//     位掩码做决定，所以新增平台永远不会逼核心长出 if/else。
//
//  4. Registry —— Type→Factory 映射，语义为"后写者胜"。新增平台就是"注册一个
//     factory"，而不是"改核心"。
//
// 边界规则（MUL-3515 决策 §2）：信封**只**放每个平台都为真的字段——Text、规范化消息
// 类型枚举、媒体引用、回复/线程锚点、路由 Source、供去重的事件/消息 id。任何平台特有
// 的东西（飞书原生 msg_type、parent_id、root_id…）都在 Raw 里，且只由产生它的适配器
// 读取。核心从不读 Raw。
//
// 本包是"纯"的：不依赖数据库、网络或任何平台，也从不 import 其它集成包。具体的
// 飞书/Lark 适配器、DB-backed 的安装/身份/会话 resolver、以及驱动 Connect/Disconnect
// 的 supervisor，在后续割接中接线（见 MUL-3515）。
package channel
