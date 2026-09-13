// connector.go：飞书长连接“事件发射器 / 事件连接器”两个核心接口，以及连接器
// 工厂类型。
package lark

import "context"

// EventEmitter 是 EventConnector 对每条已解码入站消息调用的“逐消息”回调。
// 它把规范化后的消息分发出去，并返回类型化结果和基础架构错误。
//
// connector 只对 error 作出反应：非 nil error 是真正的基础架构故障（DB 挂、
// dispatcher 配置错误），connector 应把它抛出去让引擎带着退避去重连；nil
// error 表示消息已被接收并归类（它仍可能因产品原因被丢弃——那不算错误，任何
// 判定隐含的出站回复由 runtime 在 ACK 路径之外处理）。connector 绝不能绕过
// emit 直接写 DB；emit 是唯一的入站路径。
//
// 历史上由 Feishu Hub 把 emit 传给 connector；接入与渠道无关的引擎后，改为
// feishuChannel 适配器包住 connector，并提供这样一个 emit：把每个事件规范化后
// 交给引擎 Router（经 channel.Config.Handler 注入）。
type EventEmitter func(ctx context.Context, msg InboundMessage) (DispatchResult, error)

// EventConnector 是“每个安装一个”的飞书传输层：它打开 Lark 长连接、解码事件、
// 规范化为 InboundMessage，并逐条调用 emit。Run 必须阻塞到 ctx 被取消（返回
// nil）或连接结束且本地无法恢复（返回 error）。实现必须容忍在不同 ctx 上被
// 反复 Run——引擎可能在退避后 Run、返回、再 Run。
type EventConnector interface {
	Run(ctx context.Context, inst Installation, emit EventEmitter) error
}

// ConnectorFactory 构造 EventConnector。保留给引导/兜底路径使用
// （NoopConnectorFactory）；真实 WS connector 是单个共享实例，其 Run 以
// installation 为参数。
type ConnectorFactory func(inst Installation) (EventConnector, error)
