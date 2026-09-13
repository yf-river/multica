package agent

// 文件职责：把"exec 格式错误"（ENOEXEC 及其各平台变体）翻译成可操作的修复指引。
//
// 背景：从 npm 分发的 agent CLI 在 bin 入口放一个占位文本桩，靠 postinstall 脚本
// 用平台原生二进制覆盖它——@anthropic-ai/claude-code（bin/claude.exe，各平台都用
// .exe 后缀）和 opencode-ai（bin/opencode.exe）都是这个形态。任何拦掉该脚本的安装
// 方式都会把"带可执行位的桩"留在原地：npm 12 默认拦安装脚本、pnpm 10 需要
// approve-builds、--ignore-scripts / --omit=optional 亦然。exec.LookPath 认这个桩
// （它确实是可执行文件），execve 却不认（没 shebang、没原生头），于是 CLI 在 daemon
// 各处检查里都"像装好了"，只在临门一脚失败，报错还只有一句不含原因也不含修法的
// "fork/exec <path>: exec format error"（MUL-6164）。
//
// 对外职责：ExplainExecError 给错误附上诊断，IsExecFormatError 让调用方区分
// "安装坏了（永久）"与"只是忙/升级中（暂时）"，ExecFormatRepairFor 提供机器可读
// 的重装命令（供 UI 展示）。可执行路径从错误里读回，而不是作为参数传进来——这样
// 每个边界只需一处与 provider 无关的调用，backend 内部不必层层包。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// ExplainExecError 在 OS 因"该文件不是本平台能运行的程序"（ENOEXEC）而拒绝执行
// agent CLI 时，给 err 附上可操作的诊断。其它任何错误原样返回。
//
// 可执行路径是从错误里读回、而不是传进来的，所以每个边界只需一处与 provider
// 无关的调用，而不是在每个 backend 里都包一层。
//
// 对同一个错误应用两次是空操作。边界会嵌套——backend 的前置检查错误会穿过 daemon
// 的启动边界往外走——诊断印两遍比它替代的那句裸 errno 更糟。
func ExplainExecError(err error) error {
	if err == nil || !IsExecFormatError(err) {
		return err
	}
	var explained explainedExecError
	if errors.As(err, &explained) {
		return err
	}
	return explainedExecError{fmt.Errorf("%w: %s", err, execFormatDiagnosis(execPathFromError(err)))}
}

// explainedExecError 标记一条"已经带诊断"的错误，让外层边界隔着任意层 %w 也能认出
// 自己的成果。
type explainedExecError struct{ error }

func (e explainedExecError) Unwrap() error { return e.error }

// IsExecFormatError 报告 OS 是否因"该文件不是本机可执行的程序"而拒绝运行它。
//
// 这是对"此刻就躺在磁盘上的文件"的判决，不是一次性的启动故障：同一个二进制会以
// 同样的方式一直被拒，直到有人改它。调用方据此把"永久的安装问题"与"CLI 只是忙或
// 正在升级"分开。
func IsExecFormatError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.ENOEXEC) || isPlatformExecFormatError(err)
}

// execPathFromError 从错误里找回 OS 拒绝运行的那个可执行路径。os/exec 把启动失败
// 报成 *fs.PathError（fork/exec）、把查找失败报成 *exec.Error；两者都带路径，所以
// 调用点都不用另传一份。
func execPathFromError(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Path
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return execErr.Name
	}
	return ""
}

// blockedPostinstallCauses 列出会跳过包 postinstall 的安装路径。保留在消息里，因为
// 只有用户自己的安装命令能修好它，而具体是哪一条适用，daemon 事后观察不到。
const blockedPostinstallCauses = "npm 12 allowScripts, pnpm 10 approve-builds, --ignore-scripts, --omit=optional"

// ExecFormatRepair 是诊断里机器可读的一半：哪个 npm 包拥有这个不可运行的入口，
// 以及重装它的原生二进制需要跑什么命令。
//
// daemon 在把某运行时判下线时把它报给 server，让 UI 能告诉用户"你的 agent 为什么
// 停了、该跑什么"——客户端负责把句子本地化，而只有 daemon 知道路径，所以任何一边
// 都单独凑不出这一对。Command 为空表示"没认出这种布局"，除了"重装 CLI"没有更具体
// 的可给。
type ExecFormatRepair struct {
	Package string `json:"package,omitempty"`
	Command string `json:"command,omitempty"`
	// Shell 指明 Command 是按哪个解释器写的，展示方据此如实标注代码块语言。
	// 命令按"上报它的那台机器"来渲染——它只能在那个机器上跑——而不是按随手在看的
	// 人。
	Shell string `json:"shell,omitempty"`
}

// Shells 定义修复命令可面向的解释器。Windows 用 PowerShell：它是 Windows 终端的
// 默认 shell，而另一个选择（cmd.exe）没有同时适用于 PowerShell 的写法——`cd /d`
// 不是 PowerShell 命令，PowerShell 5.1 又没有 `&&`。
const (
	shellBash       = "bash"
	shellPowerShell = "powershell"
)

// ExecFormatRepairFor 返回"OS 拒绝运行的可执行文件"对应的修复命令；当 execPath
// 不是"声明了 postinstall 的 npm 包"的 bin 入口时返回 false。
func ExecFormatRepairFor(execPath string) (ExecFormatRepair, bool) {
	return execFormatRepairForShell(execPath, hostShell())
}

// hostShell 返回当前平台适用的修复命令解释器。
func hostShell() string {
	if runtime.GOOS == "windows" {
		return shellPowerShell
	}
	return shellBash
}

func execFormatRepairForShell(execPath, shell string) (ExecFormatRepair, bool) {
	pkg, root, script, ok := npmPackagePostinstall(execPath)
	if !ok {
		return ExecFormatRepair{}, false
	}
	return ExecFormatRepair{Package: pkg, Command: repairCommand(root, script, shell), Shell: shell}, true
}

// repairCommand 把"跑这个包的 postinstall"渲染成指定 shell 的命令。
//
// postinstall 必须以包根目录为工作目录运行——npm 就是这么跑的，凡是脚本里引用
// 相对路径的（opencode 的 `node ./postinstall.mjs`）换目录就会坏——所以"切目录"
// 是命令的一部分，而不是留给读者去推断的说明。
func repairCommand(root, script, shell string) string {
	if shell == shellPowerShell {
		// 两条语句、两行输出：Windows 至今仍自带 PowerShell 5.1，它没有 `&&`；
		// Set-Location 能处理 cmd 裸 `cd` 会静默搞不定的换盘符。
		return fmt.Sprintf("Set-Location %s\n%s", powerShellQuote(root), script)
	}
	return fmt.Sprintf("cd %s && %s", shellQuote(root), script)
}

func execFormatDiagnosis(execPath string) string {
	target := execPath
	if target == "" {
		target = "the agent CLI"
	}
	repair, ok := ExecFormatRepairFor(execPath)
	if !ok {
		return fmt.Sprintf(
			"%s is not a runnable executable on %s/%s. For an agent CLI installed from npm this usually means its postinstall never ran (%s all block it), leaving the package's placeholder stub at that path. Reinstall the CLI with install scripts enabled, then retry",
			target, runtime.GOOS, runtime.GOARCH, blockedPostinstallCauses,
		)
	}
	// 这里压成一行：这是错误消息，多行的 PowerShell 修复命令会弄断它所在的日志
	// 行。逐字对外展示的是结构化修复（ExecFormatRepairFor）。
	return fmt.Sprintf(
		"%s is not a runnable executable on %s/%s. It is the bin entry of npm package %s, so the usual cause is that its postinstall never ran (%s all block it), leaving the package's placeholder stub in place. Run `%s` to install the native binary, or reinstall the CLI with install scripts enabled, then retry",
		target, runtime.GOOS, runtime.GOARCH, repair.Package, blockedPostinstallCauses,
		strings.ReplaceAll(repair.Command, "\n", "; "),
	)
}

// npmPackagePostinstall 上报拥有 execPath 的 npm 包，以及 npm 本来会为它跑的
// postinstall 命令——这样消息能点名确切的修复命令，而不是泛泛一句"重装"。
//
// 它只认会产生这种失败的那一种布局——bin/ 入口点直接位于包根之下——并从包自己的
// manifest 里读出修复命令。这让它保持 provider 无关：claude 的 `node install.cjs`
// 和 opencode 的 `node ./postinstall.mjs` 都只是 scripts.postinstall 里写了什么，
// 以后新增的 CLI 不需要改这里。
func npmPackagePostinstall(execPath string) (name, root, script string, ok bool) {
	if execPath == "" {
		return "", "", "", false
	}
	dir := filepath.Dir(execPath)
	if !strings.EqualFold(filepath.Base(dir), "bin") {
		return "", "", "", false
	}
	root = filepath.Dir(dir)
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return "", "", "", false
	}
	var manifest struct {
		Name    string `json:"name"`
		Scripts struct {
			Postinstall string `json:"postinstall"`
		} `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", "", "", false
	}
	script = strings.TrimSpace(manifest.Scripts.Postinstall)
	if script == "" {
		return "", "", "", false
	}
	name = strings.TrimSpace(manifest.Name)
	if name == "" {
		name = filepath.Base(root)
	}
	return name, root, script, true
}

// shellQuote 让任意路径能安全粘进 POSIX shell。修复命令是要被复制原样跑的，所以
// 含引号或空格的路径必须能完整熬过这趟往返。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// powerShellQuote 是对 PowerShell 的同一保证：单引号字符串里的单引号靠翻倍来转义。
// Windows 路径正是需要它的地方："C:\\Program Files\\..." 必须能粘进去，带撇号的
// 用户目录也一样。
func powerShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
