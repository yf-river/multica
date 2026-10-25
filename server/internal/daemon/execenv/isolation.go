// 隔离执行环境准备工作：把 Prepare/Reuse 放到一个独立、可强杀的 helper
// 子进程里执行，而不是在当前 daemon 进程内跑。这样当文件系统系统调用被
// 卡住时可以直接终止整个进程树，不会残留一个"任务已被重试、却可能继续
// 往磁盘写入"的进程内 goroutine。

package execenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// PreparationHelperArg 选择 multica 二进制里的"私有执行环境准备"子命令。
// daemon 通过这个子进程跑 Prepare/Reuse：被阻塞的文件系统调用可以被杀死，
// 而不会留下一个可能在任务被重试后仍恢复写入的进程内 goroutine。
const PreparationHelperArg = "__multica_execenv_prepare"

const (
	preparationActionPrepare = "prepare"
	preparationActionReuse   = "reuse"
	preparationWaitDelay     = 2 * time.Second
)

// preparationRequest 是发给 helper 子进程的请求：要么 prepare、要么 reuse，
// 两者互斥，由 Action 区分。
type preparationRequest struct {
	Action  string         `json:"action"`
	Prepare *PrepareParams `json:"prepare,omitempty"`
	Reuse   *ReuseParams   `json:"reuse,omitempty"`
}

// preparationResponse 是 helper 写回的结果：成功时带 Environment，
// 失败时把错误文本放进 Error，让父进程能原样保留错误信息。
type preparationResponse struct {
	Environment *Environment `json:"environment,omitempty"`
	Error       string       `json:"error,omitempty"`
}

// PrepareIsolated 在一个可被强杀的 helper 子进程中执行 Prepare。
// command 在生产环境应指向"当前 multica 可执行文件 + PreparationHelperArg"；
// 接收命令切片也让测试可以用 go test 的测试二进制当 helper，而不必装 CLI。
func PrepareIsolated(ctx context.Context, command []string, params PrepareParams, logger *slog.Logger) (*Environment, error) {
	return runPreparationProcess(ctx, command, preparationRequest{
		Action:  preparationActionPrepare,
		Prepare: &params,
	}, logger)
}

// ReuseIsolated 与 PrepareIsolated 相同的"可杀 helper 子进程"契约下执行 Reuse。
func ReuseIsolated(ctx context.Context, command []string, params ReuseParams, logger *slog.Logger) (*Environment, error) {
	return runPreparationProcess(ctx, command, preparationRequest{
		Action: preparationActionReuse,
		Reuse:  &params,
	}, logger)
}

// runPreparationProcess 是 PrepareIsolated/ReuseIsolated 的公共实现：
// 把请求 JSON 编码后从 stdin 喂给 helper，等它执行完并把 Environment
// （或错误）从 stdout 以 JSON 形式回传。上下文取消时通过 controller 强杀
// 整个 helper 进程树。
func runPreparationProcess(ctx context.Context, command []string, request preparationRequest, logger *slog.Logger) (*Environment, error) {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return nil, errors.New("execenv: preparation helper command is empty")
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	payload, err := marshalPreparationRequest(request)
	if err != nil {
		return nil, fmt.Errorf("execenv: encode preparation request: %w", err)
	}

	cmd := exec.Command(command[0], command[1:]...)
	controller, err := newPreparationProcessController(cmd)
	if err != nil {
		return nil, fmt.Errorf("execenv: create preparation process controller: %w", err)
	}
	defer controller.close()
	cmd.WaitDelay = preparationWaitDelay
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("execenv: create preparation stdin: %w", err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("execenv: start preparation helper: %w", err)
	}
	// helper 阻塞等待读取 stdin。要在释放有限大小的请求体之前，先把它挂到
	// 平台的进程树边界（unix 的进程组 / windows 的 job object）上——
	// 否则在 Start 成功到完成归属设置之间的空隙里，它可能又派生出子孙进程。
	if err := controller.attach(cmd); err != nil {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = controller.finish()
		return nil, fmt.Errorf("execenv: attach preparation helper process: %w", err)
	}

	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := stdin.Write(payload)
		closeErr := stdin.Close()
		writeDone <- errors.Join(writeErr, closeErr)
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var stopErr error
	select {
	case err = <-waitDone:
	case <-ctx.Done():
		// 请求方取消：强杀 helper 进程树，然后等 Wait 返回收尸。
		stopErr = controller.stop(cmd)
		err = <-waitDone
	}
	writeErr := <-writeDone
	finishErr := controller.finish()
	if lifecycleErr := errors.Join(stopErr, finishErr); lifecycleErr != nil {
		return nil, fmt.Errorf("execenv: stop preparation process tree: %w", lifecycleErr)
	}
	// context 的取消原因是 daemon 对外稳定的失败契约，优先用它，
	// 而不是平台相关的进程退出文本（如 "signal: killed"、退出码 1 等）。
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	if err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return nil, fmt.Errorf("execenv: preparation helper failed: %w: %s", err, detail)
		}
		return nil, fmt.Errorf("execenv: preparation helper failed: %w", err)
	}
	if writeErr != nil {
		return nil, fmt.Errorf("execenv: write preparation request: %w", writeErr)
	}

	var response preparationResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return nil, fmt.Errorf("execenv: decode preparation response: %w", err)
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	if response.Environment != nil {
		// logger 有意不放进 JSON。这里把属主 daemon 的 logger 重新挂回去，
		// 让后续清理时仍能走正常的诊断日志。
		response.Environment.logger = logger
	}
	return response.Environment, nil
}

// marshalPreparationRequest 把请求编码成 JSON（独立成函数便于两端一致复用）。
func marshalPreparationRequest(request preparationRequest) ([]byte, error) {
	return json.Marshal(request)
}

// decodePreparationRequest 读取 helper 请求；升级中的旧父进程可能携带新 helper 不再使用的字段。
func decodePreparationRequest(in io.Reader) (preparationRequest, error) {
	var request preparationRequest
	if err := json.NewDecoder(in).Decode(&request); err != nil {
		return preparationRequest{}, err
	}
	return request, nil
}

// RunPreparationHelper 在 stdin/stdout 上服务这个私有 helper 协议（由
// helper 子进程执行）。Prepare 产生的"业务错误"会编码进响应，让父进程能
// 原样保留；而协议本身的读写错误直接以进程错误返回，因为父进程无法安全
// 解读这种结果。
func RunPreparationHelper(in io.Reader, out io.Writer, logger *slog.Logger) error {
	request, err := decodePreparationRequest(in)
	if err != nil {
		return fmt.Errorf("decode preparation request: %w", err)
	}

	var response preparationResponse
	switch request.Action {
	case preparationActionPrepare:
		// prepare 与 reuse 的请求参数互斥，交叉出现说明协议被破坏。
		if request.Prepare == nil || request.Reuse != nil {
			return errors.New("invalid prepare request")
		}
		env, err := Prepare(*request.Prepare, logger)
		response.Environment = env
		if err != nil {
			response.Error = err.Error()
		}
	case preparationActionReuse:
		if request.Reuse == nil || request.Prepare != nil {
			return errors.New("invalid reuse request")
		}
		response.Environment = Reuse(*request.Reuse, logger)
	default:
		return fmt.Errorf("unknown preparation action %q", request.Action)
	}

	if err := json.NewEncoder(out).Encode(response); err != nil {
		return fmt.Errorf("encode preparation response: %w", err)
	}
	return nil
}
