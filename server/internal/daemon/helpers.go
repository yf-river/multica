package daemon

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 本文件是一组小的环境变量/时长/布尔解析与可取消睡眠工具函数，供守护进程配置
// 装配与轮询循环使用。dayUnit/parseFlexDuration 是给运维用的"人类可读时长"语法
// （含标准库不支持的 d 天数单位）；sleepWithContext 系列让睡眠能被 ctx 取消或
// 被唤醒 channel 打断。

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func durationFromEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	d, err := parseFlexDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", key, value, err)
	}
	return d, nil
}

// dayUnit 匹配一个十进制数（小数部分可选）后跟 `d`（天），因此 "5d" 与 "1.5d"
// 都能整段捕获并展开为小时。
var dayUnit = regexp.MustCompile(`(\d*\.\d+|\d+)d`)

// parseFlexDuration 接受标准 Go time.ParseDuration 语法外加一个 `d`（天）后缀，
// 后者会被标准库拒绝。"5d" → 120h，"1d12h" → 36h，"0.5d" → 12h。溢出或畸形
// 数字以错误形式传播。
func parseFlexDuration(value string) (time.Duration, error) {
	var convErr error
	expanded := dayUnit.ReplaceAllStringFunc(value, func(match string) string {
		days, err := strconv.ParseFloat(match[:len(match)-1], 64)
		if err != nil {
			convErr = err
			return match
		}
		// time.ParseDuration 原生处理小数小时，并自行拒绝溢出。
		return strconv.FormatFloat(days*24, 'f', -1, 64) + "h"
	})
	if convErr != nil {
		return 0, convErr
	}
	return time.ParseDuration(expanded)
}

// boolFromEnv 读取一个布尔环境变量覆盖，变量未设置或带有无法识别的 token 时
// 返回 fallback。接受（大小写不敏感）：true/1/yes/on 与 false/0/no/off。
func boolFromEnv(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "false", "0", "no", "off":
		return false
	case "true", "1", "yes", "on":
		return true
	}
	return fallback
}

func intFromEnv(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q: %w", key, value, err)
	}
	return n, nil
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func sleepWithContextOrWakeup(ctx context.Context, d time.Duration, wakeups <-chan struct{}) error {
	if wakeups == nil {
		return sleepWithContext(ctx, d)
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wakeups:
		return nil
	case <-timer.C:
		return nil
	}
}
