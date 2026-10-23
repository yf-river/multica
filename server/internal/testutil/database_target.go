package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// TestDatabaseURL 在建立连接和清理 fixture 前复用统一测试目标检查器。
// 检查器拒绝固定环境实例、冻结事故实例和通过 DSN 参数覆盖目标的连接。
func TestDatabaseURL() (string, error) {
	target := os.Getenv("DATABASE_URL")
	if target == "" || os.Getenv("MULTICA_TEST_DATABASE_URL") == "" {
		return "", fmt.Errorf("测试必须显式设置 DATABASE_URL 和 MULTICA_TEST_DATABASE_URL；纯编译使用 go test -c")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("无法定位测试数据库检查器")
	}
	script := filepath.Join(filepath.Dir(file), "../../../scripts/check-test-database.py")
	if info, err := os.Stat(script); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("测试数据库检查器缺失；请在完整 checkout 中运行测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "python3", script).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("测试数据库目标核对超时，未建立连接")
		}
		return "", fmt.Errorf("测试数据库目标未通过检查：%s", strings.TrimSpace(string(output)))
	}
	return target, nil
}
