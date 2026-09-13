// cron.go —— 服务端 cron 解析与下一次/未来发生时刻计算（UTC 规范化）。
// 调度决策一律基于 DB 时间，杜绝时钟歪斜的多个 app 实例产生分歧。
package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser 接受标准 5 字段 cron 表达式。
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// NextOccurrenceAfterUTC 在指定 IANA 时区内解析 cronExpr，返回严格晚于 `after` 的下一次激活。结果始终为 UTC，
// 代表下一次发生的规范触发时刻。
//
// `after` 按绝对时刻解释；调用方应传 DB 时间（如 `SELECT now()`）而非 `time.Now()`，使时钟歪斜的两个
// 应用实例仍得出相同答案。
//
// 这是新 scheduler.AutopilotScheduleDispatchJob 计算 plan_times 的构件；handler/UI 写路径经
// ComputeNextRunFromUTC 用它回填仅供展示的 autopilot_trigger.next_run_at 列。
func NextOccurrenceAfterUTC(cronExpr, timezone string, after time.Time) (time.Time, error) {
	sched, loc, err := parseCronSchedule(cronExpr, timezone)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(after.In(loc)).UTC(), nil
}

// NextOccurrencesAfterUTC 在 `timezone` 中解析 cronExpr 一次，返回严格晚于 `after` 的下 `count` 次激活（升序、UTC）。
// 表达式在 robfig 五年搜索范围内再无发生（如 "0 0 30 2 *"，robfig 报零时）时提前结束、返回更短切片。
//
// 与循环调用 NextOccurrenceAfterUTC 不同，表达式只解析一次、时区只加载一次。调度器编辑器的预览端点在每次
// 防抖击键时都问它，这个差别不是学术性的。
func NextOccurrencesAfterUTC(cronExpr, timezone string, after time.Time, count int) ([]time.Time, error) {
	sched, loc, err := parseCronSchedule(cronExpr, timezone)
	if err != nil {
		return nil, err
	}
	out := make([]time.Time, 0, count)
	cursor := after.In(loc)
	for i := 0; i < count; i++ {
		next := sched.Next(cursor)
		if next.IsZero() {
			break
		}
		out = append(out, next.UTC())
		cursor = next
	}
	return out, nil
}

// NextOccurrencesUTC 在 `timezone` 中解析 cronExpr，返回半开区间 `(after, until]` 内的每次激活，按规范 UTC 升序。
// Autopilot 调度派发任务用它枚举“自上次存储发生点以来到期”的每个 plan_time。
//
// 切片上限 1024 条——防止意外 “每秒一次” 的 cron 在多日补跑窗口上失控。调度器管理器还会把返回切片
// 再截到 JobSpec.MaxPlansPerTick。
func NextOccurrencesUTC(cronExpr, timezone string, after, until time.Time) ([]time.Time, error) {
	sched, loc, err := parseCronSchedule(cronExpr, timezone)
	if err != nil {
		return nil, err
	}
	const hardCap = 1024
	out := make([]time.Time, 0, 8)
	cursor := after.In(loc)
	untilLocal := until.In(loc)
	for len(out) < hardCap {
		next := sched.Next(cursor)
		if next.After(untilLocal) {
			break
		}
		out = append(out, next.UTC())
		cursor = next
	}
	return out, nil
}

// ComputeNextRun 用应用本地 now() 求值 cron，回填仅供展示的 autopilot_trigger.next_run_at 列（触发器创建/更新
// handler 与失败监视器用）。展示值刻意用本地钟：NTP 下应用/DB 时钟偏差远低于该列分钟级粒度，为这些 UI 写路径
// 穿透 DB 时间换不来用户可见的精度。
//
// 调度器的派发后推进仍把 next_run_at 放在同一本地钟上，但以 max(now, plan_time) 为锚调 NextOccurrenceAfterUTC
// 而非本助手，落后的应用钟绝不会把该列重新指向刚触发过的槽位（见 scheduler.advancedNextRun / autopilotHandler，
// MUL-3749）。
//
// 调度决策是另一回事，必须走 NextOccurrencesUTC / NextOccurrenceAfterUTC 用 DB 时间：跨时钟歪斜实例的派发
// 正确性依赖它。
func ComputeNextRun(cronExpr, timezone string) (time.Time, error) {
	return NextOccurrenceAfterUTC(cronExpr, timezone, time.Now())
}

// ValidateTimezone 无法识别时区字符串时返回错误。
func ValidateTimezone(timezone string) error {
	_, err := time.LoadLocation(timezone)
	if err != nil {
		return fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	return nil
}

// parseCronSchedule 解析 cron 并加载时区；对 robfig 解析器会 panic 的形态先拒绝（见内联注释）。
func parseCronSchedule(cronExpr, timezone string) (cron.Schedule, *time.Location, error) {
	// robfig v3.0.1 会读取到首个空格为止的可选 "TZ="/"CRON_TZ=" 前缀，空格缺失时 panic（parser.go:99, slice[:-1]）。
	// 预览端点把原始用户文本喂到这里，因此先拒绝解析器无法存活的形态，而不是把笔误变成 500。
	if (strings.HasPrefix(cronExpr, "TZ=") || strings.HasPrefix(cronExpr, "CRON_TZ=")) &&
		!strings.Contains(cronExpr, " ") {
		return nil, nil, fmt.Errorf("parse cron: missing schedule after timezone prefix %q", cronExpr)
	}
	sched, err := cronParser.Parse(cronExpr)
	if err != nil {
		return nil, nil, fmt.Errorf("parse cron: %w", err)
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	return sched, loc, nil
}
