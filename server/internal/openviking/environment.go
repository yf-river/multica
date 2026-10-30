package openviking

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// DeploymentIdentity 是既有生产部署记录中的非敏感存储身份，由环境控制器从真实配置生成。
type DeploymentIdentity struct {
	Enabled      bool   `json:"enabled"`
	URL          string `json:"url"`
	DataRoot     string `json:"data_root"`
	ArchiveRoot  string `json:"archive_root"`
	LocalStorage bool   `json:"local_storage"`
}

// ValidateEnvironment 在任何数据库采集、同步或原生请求之前检查专项验收边界。
func ValidateEnvironment() error {
	value := os.Getenv("MULTICA_OPENVIKING_ENABLED")
	if value != "" && value != "true" && value != "false" {
		return fmt.Errorf("MULTICA_OPENVIKING_ENABLED 必须为 true 或 false")
	}
	if !Enabled() {
		return nil
	}
	if os.Getenv("MULTICA_ENV_ID") == "prod" && os.Getenv("MULTICA_LOCAL_DEV") != "1" {
		return nil
	}
	if os.Getenv("MULTICA_OPENVIKING_ACCEPTANCE") != "true" {
		return fmt.Errorf("OpenViking 在非生产环境默认关闭；开启必须显式声明 MULTICA_OPENVIKING_ACCEPTANCE=true")
	}
	var production struct {
		Environment string             `json:"environment"`
		OpenViking  DeploymentIdentity `json:"openviking"`
	}
	if err := readEnvironmentJSON(os.Getenv("MULTICA_OPENVIKING_PRODUCTION_DEPLOYMENT"), &production); err != nil {
		return fmt.Errorf("无法确认生产 OpenViking 身份: %w", err)
	}
	protected := production.OpenViking
	if production.Environment != "prod" || !protected.Enabled || !protected.LocalStorage || protected.URL == "" || protected.DataRoot == "" || protected.ArchiveRoot == "" {
		return fmt.Errorf("生产部署记录缺少可核对的 OpenViking 实例与本地存储身份")
	}
	var config struct {
		Server struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"server"`
		Storage struct {
			Workspace string         `json:"workspace"`
			AGFS      map[string]any `json:"agfs"`
			VectorDB  map[string]any `json:"vectordb"`
		} `json:"storage"`
	}
	file := os.Getenv("MULTICA_OPENVIKING_CONFIG_FILE")
	if err := readEnvironmentJSON(file, &config); err != nil {
		return fmt.Errorf("无法确认专项 OpenViking 配置: %w", err)
	}
	endpoint, err := url.Parse(os.Getenv("MULTICA_OPENVIKING_URL"))
	if err != nil || endpoint == nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Path != "" && endpoint.Path != "/" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fmt.Errorf("专项 OpenViking 必须使用可核对的本机独立 HTTP 实例")
	}
	host := endpoint.Hostname()
	if host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		return fmt.Errorf("无法确认远端专项 OpenViking 存储隔离，拒绝接入")
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || port != config.Server.Port || port <= 0 || config.Server.Host != "127.0.0.1" && config.Server.Host != "::1" && config.Server.Host != "localhost" {
		return fmt.Errorf("专项 OpenViking 地址与原生进程绑定配置不一致")
	}
	prodURL, err := url.Parse(protected.URL)
	if err != nil || prodURL.Hostname() == "" {
		return fmt.Errorf("生产 OpenViking 地址无效")
	}
	// 本机别名及缺省端口不能冒充独立生产实例。
	prodPort := prodURL.Port()
	if prodPort == "" {
		if prodURL.Scheme == "https" {
			prodPort = "443"
		} else {
			prodPort = "80"
		}
	}
	if endpoint.Port() == prodPort {
		return fmt.Errorf("专项 OpenViking 不得连接生产实例或端口别名")
	}
	for _, backend := range []map[string]any{config.Storage.AGFS, config.Storage.VectorDB} {
		if len(backend) != 1 || backend["backend"] != "local" {
			return fmt.Errorf("专项验收仅支持工作目录内的本地 AGFS 和向量存储；无法确认外部或覆盖存储的隔离")
		}
	}
	own := []string{config.Storage.Workspace, os.Getenv("MULTICA_OPENVIKING_ARCHIVE_ROOT")}
	foreign := []string{protected.DataRoot, protected.ArchiveRoot}
	for _, a := range own {
		for _, b := range foreign {
			if err := separateStorage(a, b); err != nil {
				return err
			}
		}
	}
	if err := separateStorage(own[0], own[1]); err != nil {
		return err
	}
	if err := nativeProcessUsesConfig(file); err != nil {
		return err
	}
	return nil
}

func readEnvironmentJSON(path string, target any) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("必须提供配置或部署记录的绝对路径")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func separateStorage(first, second string) error {
	if !filepath.IsAbs(first) || !filepath.IsAbs(second) {
		return fmt.Errorf("OpenViking 存储必须使用明确的绝对路径")
	}
	a, err := filepath.EvalSymlinks(first)
	if err != nil {
		return fmt.Errorf("无法确认 OpenViking 存储: %w", err)
	}
	b, err := filepath.EvalSymlinks(second)
	if err != nil {
		return fmt.Errorf("无法确认生产或验收存储: %w", err)
	}
	sa, err := os.Stat(a)
	if err != nil {
		return err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return err
	}
	if !sa.IsDir() || !sb.IsDir() {
		return fmt.Errorf("OpenViking 存储必须是已存在的目录")
	}
	overlap := func(a, b string) bool {
		return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
	}
	if os.SameFile(sa, sb) || overlap(a, b) || overlap(physicalStorage(a), physicalStorage(b)) {
		return fmt.Errorf("OpenViking 验收数据或归档与生产/另一存储目录重叠")
	}
	return nil
}

// Linux 的挂载根用于识别 bind mount；目录名称不同不代表存储不同。
func physicalStorage(path string) string {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return path
	}
	best := ""
	result := path
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		mount := unescape.Replace(fields[4])
		if path != mount && !strings.HasPrefix(path, strings.TrimRight(mount, "/")+"/") {
			continue
		}
		if len(mount) <= len(best) {
			continue
		}
		best = mount
		relative, _ := filepath.Rel(mount, path)
		result = fields[2] + ":" + filepath.Join(unescape.Replace(fields[3]), relative)
	}
	return result
}

// 专项原生实例须以明确 --config 启动。未知进程、容器或不可核对的配置拒绝接入。
func nativeProcessUsesConfig(file string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("当前平台无法确认专项 OpenViking 进程与存储，拒绝接入")
	}
	expected, err := filepath.EvalSymlinks(file)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(string(raw), "\x00")
		native := false
		for _, arg := range args {
			if filepath.Base(arg) == "openviking-server" {
				native = true
			}
		}
		if !native {
			continue
		}
		for i, arg := range args {
			if arg == "--config" && i+1 < len(args) {
				actual, err := filepath.EvalSymlinks(args[i+1])
				if err == nil && actual == expected {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("找不到使用指定配置的独立 OpenViking 进程")
}
