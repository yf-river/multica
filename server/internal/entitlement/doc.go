// Package entitlement 从 Multica Cloud 拉取并缓存「按 workspace 生效的执行限制
// 策略」。它刻意只做机械的策略消费方：套餐名、订阅解读、商业额度都由 Cloud 解析，
// 这里从不出现。
//
// 零值 Config 表示「已禁用」。禁用、不可用、非法或过期的策略一律返回 ActionOff，
// 因此仅仅 import 或构造本包都不可能改变产品行为。配置了 MULTICA_CLOUD_URL 后，
// 生产环境的消费者才会真正连上。
package entitlement
