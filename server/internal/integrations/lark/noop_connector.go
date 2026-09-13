// noop_connector.go：一个“占位”连接器——不拨号、不下发任何入站事件，只把
// run 的 ctx 挂起直到取消。
package lark

import (
	"context"
	"log/slog"
)

// NoopConnector 通过“什么都不拨、什么都不下发”来满足 EventConnector：它只是
// 把 run 的 ctx 一直挂着。存在的意义是让 Hub 在真实飞书长连接落地之前就能接入
// 启动路径：租约生命周期、supervisor / renewer goroutine、清扫与关闭等管道都
// 对着真实 DB 跑，且一旦有安装，操作者就能看到 Hub 的“lease acquired”日志——
// 而不是让 Hub 对一个半成品真实 connector 反复重试，看起来像线上协议坏了，
// 其实根本还没实现。
//
// MVP 的下一阶段（MUL-2671）会用真正打开 Lark WebSocket 长连接、解码事件并
// 调用 emit 的 connector 替换它。届时 router.go 里的 NoopConnectorFactory 调用
// 点换成真实工厂即可，Hub 本身不用改。
type NoopConnector struct {
	logger *slog.Logger
}

// NewNoopConnector 返回一个阻塞到 Hub 取消其 run ctx 的 connector。logger 可为
// nil；调用方通常传 slog.Default。
func NewNoopConnector(logger *slog.Logger) *NoopConnector {
	if logger == nil {
		logger = slog.Default()
	}
	return &NoopConnector{logger: logger}
}

// Run 阻塞到 ctx 被取消，然后返回 nil。返回 nil 告诉 Hub 连接是干净结束的
// （关闭/租约丢失时不会触发退避重试风暴）。因为 Run 永不报错且只要租约在就
// 一直跑，Hub 的 "uptime >= ResetBackoffAfter" 分支会在每个 supervisor 周期
// 重置退避——对一个占位实现来说正合适。
func (c *NoopConnector) Run(ctx context.Context, inst Installation, _ EventEmitter) error {
	c.logger.Info("lark noop connector: holding lease (real long-conn not yet implemented)",
		"installation_id", uuidString(inst.ID),
		"app_id", inst.AppID,
	)
	<-ctx.Done()
	c.logger.Info("lark noop connector: exiting on ctx cancel",
		"installation_id", uuidString(inst.ID),
	)
	return nil
}

// NoopConnectorFactory 返回一个让所有安装共享同一个（带给定 logger 的）
// NoopConnector 的 ConnectorFactory。引导阶段由 router 使用；线上协议实现落地
// 后换成真实 connector 工厂。
func NoopConnectorFactory(logger *slog.Logger) ConnectorFactory {
	c := NewNoopConnector(logger)
	return func(_ Installation) (EventConnector, error) {
		return c, nil
	}
}
