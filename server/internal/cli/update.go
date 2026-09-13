// update.go 实现 multica CLI 的自更新逻辑：通过 Homebrew（`brew upgrade`）或直接从
// GitHub Releases 下载新二进制并原地替换当前可执行文件。直接下载路径必须先校验
// GoReleaser 发布的 checksums.txt 里的 SHA-256，因为自动更新常无人值守地跑，未经
// 校验的二进制替换属于供应链风险。
package cli

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/selfexec"
)

// ChecksumManifestName 是 GoReleaser 发布校验和清单时用的资产名（对应 .goreleaser.yml
// 里的 `checksum.name_template: "checksums.txt"`）。做成常量而不是内联，将来要改名
// 只动这一处。
const ChecksumManifestName = "checksums.txt"

// DefaultUpdateDownloadTimeout 是下载更新包时的默认超时。
const DefaultUpdateDownloadTimeout = 120 * time.Second

// GitHubRelease 是 GitHub releases API 响应里我们需要的字段子集。
type GitHubRelease struct {
	TagName string               `json:"tag_name"`
	HTMLURL string               `json:"html_url"`
	Assets  []GitHubReleaseAsset `json:"assets"`
}

// IsReleaseVersion 报告 v 是否是「打 tag 的发布版本」形态（如 "0.1.13"、"v0.1.13"），
// 而不是开发构建（空版本号或 `git describe` 风格的 "v0.2.15-235-gdaf0e935"）。
// 自动更新轮询用它跳过「源码构建」的自更新——把源码构建降级成公开 release 会覆盖掉
// 未发布的改动。
func IsReleaseVersion(v string) bool {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if s == "" {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// IsNewerVersion 报告 latest 是否严格新于 current。两侧都可以带可选的 "v" 前缀；
// 非数字尾巴（第 4 段、预发布标签等）会被忽略。任一侧解析失败都返回 false——
// 调用方把这种情况当作「保持现状」。
func IsNewerVersion(latest, current string) bool {
	l, ok := parseReleaseVersion(latest)
	if !ok {
		return false
	}
	c, ok := parseReleaseVersion(current)
	if !ok {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// parseReleaseVersion 提取 v 的三个数字段。成功返回 (parts, true)；v 缺失、畸形或带
// 任何非数字尾巴（dev-describe 后缀、第 4 段、预发布标签等）时返回 (_, false)。
// 严格要求是有意的：这是 IsNewerVersion 用的唯一解析器，autoUpdateLoop 绝不能仅仅
// 因为 dev-describe 的补丁号去掉后缀后碰巧像数字，就静默把开发构建降级成公开 release。
func parseReleaseVersion(v string) ([3]int, bool) {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		if p == "" {
			return [3]int{}, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return [3]int{}, false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// GitHubReleaseAsset 描述 release 里的一个附件资产。
type GitHubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// releaseArchiveExtension 按平台返回发布包的后缀：Windows 用 zip，其余用 tar.gz。
func releaseArchiveExtension(goos string) string {
	if goos == "windows" {
		return "zip"
	}
	return "tar.gz"
}

// normalizeReleaseTag 把目标版本号规整成 tag 形态：不带 "v" 前缀就补上。
func normalizeReleaseTag(targetVersion string) string {
	tag := strings.TrimSpace(targetVersion)
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	return tag
}

// releaseAssetCandidates 列出给定平台/架构对应的发布资产名。优先新版命名（带版本号，
// 现行方案）；找不到再退回旧命名 `multica_{os}_{arch}`（兼容仍按旧名发布的 release）。
func releaseAssetCandidates(targetVersion, goos, goarch string) []string {
	tag := normalizeReleaseTag(targetVersion)
	version := strings.TrimPrefix(tag, "v")
	ext := releaseArchiveExtension(goos)
	// 优先带版本号的名字（现行方案）；退回老的 `multica_{os}_{arch}` 名字，兼容
	// 仍按旧方案发布的 release。
	return []string{
		fmt.Sprintf("multica-cli-%s-%s-%s.%s", version, goos, goarch, ext),
		fmt.Sprintf("multica_%s_%s.%s", goos, goarch, ext),
	}
}

// findReleaseAsset 在 release 的资产里找当前平台/架构对应的那个下载包。
func findReleaseAsset(assets []GitHubReleaseAsset, targetVersion, goos, goarch string) (*GitHubReleaseAsset, error) {
	for _, candidate := range releaseAssetCandidates(targetVersion, goos, goarch) {
		for i := range assets {
			if assets[i].Name == candidate {
				return &assets[i], nil
			}
		}
	}

	candidates := strings.Join(releaseAssetCandidates(targetVersion, goos, goarch), ", ")
	return nil, fmt.Errorf("no matching release asset for %s/%s (tried: %s)", goos, goarch, candidates)
}

// findChecksumManifestAsset 在 release 资产里定位 GoReleaser 生成的 checksums.txt。
// 直接下载路径做 SHA-256 校验需要它——缺失就拒绝替换二进制、绝不退回「不校验直接装」，
// 因为自动更新轮询无人值守，未经校验的二进制替换是供应链风险。
func findChecksumManifestAsset(assets []GitHubReleaseAsset) (*GitHubReleaseAsset, error) {
	for i := range assets {
		if assets[i].Name == ChecksumManifestName {
			return &assets[i], nil
		}
	}
	return nil, fmt.Errorf("checksum manifest %q not present in release", ChecksumManifestName)
}

// parseChecksumManifest 解析 GoReleaser 风格的 "<sha256>  <filename>" 清单，返回
// assetName 对应的小写 hex SHA-256。资产不在清单里就报错——这样拼错文件名（或从别的
// release 拿到错误的清单）会「关死」，而不是静默关掉校验。
func parseChecksumManifest(manifest []byte, assetName string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(manifest))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		// GoReleaser 默认用两个空格作分隔，有些工具用一个空格或补制表符；
		// strings.Fields 能一次性处理所有这些情况。
		if len(fields) < 2 {
			continue
		}
		if fields[1] == assetName {
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read checksum manifest: %w", err)
	}
	return "", fmt.Errorf("checksum for %q not found in manifest", assetName)
}

// verifyAssetSHA256 在 data 的 SHA-256 与期望的小写 hex 值一致时返回 nil，否则返回
// 错误。错误信息里带上两个摘要，这样不用重新下载，光看日志就能判断资产是否损坏。
func verifyAssetSHA256(data []byte, expectedHex, assetName string) error {
	if expectedHex == "" {
		return fmt.Errorf("empty expected checksum for %q", assetName)
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if !strings.EqualFold(actual, expectedHex) {
		return fmt.Errorf("checksum mismatch for %q: expected %s, got %s", assetName, expectedHex, actual)
	}
	return nil
}

// fetchReleaseByTag 向 GitHub releases API 拉取指定 tag 的 release 元数据。
func fetchReleaseByTag(tag string) (*GitHubRelease, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/multica-ai/multica/releases/tags/"+tag, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

// FetchLatestRelease 从 multica GitHub 仓库拉取最新 release 的 tag。
func FetchLatestRelease() (*GitHubRelease, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/multica-ai/multica/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

// knownBrewPrefixes 列出 Homebrew 在各平台的安装根。顺序无所谓——这些前缀之间不嵌套。
var knownBrewPrefixes = []string{"/opt/homebrew", "/usr/local", "/home/linuxbrew/.linuxbrew"}

// MatchKnownBrewPrefix 返回 Cellar 目录下包含 path 的 Homebrew 前缀；path 不在任何
// 已知 Cellar 下就返回 ""。它是 `brew --prefix` 的离线等价物：当 `brew --prefix`
// 不可用（brew 不在 PATH 上）但二进制路径仍能暴露安装根时用。
func MatchKnownBrewPrefix(path string) string {
	for _, prefix := range knownBrewPrefixes {
		if strings.HasPrefix(path, prefix+"/Cellar/") {
			return prefix
		}
	}
	return ""
}

// IsBrewInstall 判断正在运行的 multica 二进制是否由 Homebrew 安装。
func IsBrewInstall() bool {
	exePath, err := selfexec.Resolve()
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		resolved = exePath
	}

	brewPrefix := GetBrewPrefix()
	if brewPrefix != "" && strings.HasPrefix(resolved, brewPrefix) {
		return true
	}

	return MatchKnownBrewPrefix(resolved) != ""
}

// GetBrewPrefix 通过执行 `brew --prefix` 返回 Homebrew 前缀，失败返回空串。
// 必须有界：一个卡死的 brew（坏掉的 shellenv、连不上的 home 网络）不能把调用方
// 挂住——daemon 的重载循环会周期性地走到这里。
func GetBrewPrefix() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "brew", "--prefix")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// UpdateViaBrew 执行 `brew upgrade multica-ai/tap/multica`，返回合并的输出与错误。
func UpdateViaBrew() (string, error) {
	cmd := exec.Command("brew", "upgrade", "multica-ai/tap/multica")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("brew upgrade failed: %w", err)
	}
	return string(out), nil
}

// updateDownloadTimeoutOrDefault 超时非正（未显式指定）时用默认下载超时。
func updateDownloadTimeoutOrDefault(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return DefaultUpdateDownloadTimeout
	}
	return timeout
}

// fetchURLBytes 用给定的超时发一次 GET 并完整返回响应体。它用于下载校验和清单
// （很小）与发布包（几 MB）。因为走 SHA-256 校验就必须把整段字节缓冲下来，所以
// 流式下载在这里只是把缓冲推给调用方，并无意义。
func fetchURLBytes(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: updateDownloadTimeoutOrDefault(timeout)}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return io.ReadAll(resp.Body)
}

// UpdateViaDownload 从 GitHub 下载指定版本的二进制并原地替换当前可执行文件。
// 返回合并的输出信息与错误。
func UpdateViaDownload(targetVersion string) (string, error) {
	return UpdateViaDownloadWithTimeout(targetVersion, DefaultUpdateDownloadTimeout)
}

// UpdateViaDownloadWithTimeout 用调用方选择的下载超时执行同样的「下载并替换」。
func UpdateViaDownloadWithTimeout(targetVersion string, downloadTimeout time.Duration) (string, error) {
	// 先确定当前二进制路径。
	exePath, err := selfexec.Resolve()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return "", fmt.Errorf("resolve symlink: %w", err)
	}

	tag := normalizeReleaseTag(targetVersion)
	release, err := fetchReleaseByTag(tag)
	if err != nil {
		return "", fmt.Errorf("fetch release metadata: %w", err)
	}
	asset, err := findReleaseAsset(release.Assets, tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	manifestAsset, err := findChecksumManifestAsset(release.Assets)
	if err != nil {
		return "", err
	}
	downloadURL := asset.BrowserDownloadURL
	assetName := asset.Name

	// 先拉校验和清单：如果 release 只发布了一半（压缩包传了、checksums.txt 还没传），
	// 在白白吃掉压缩包带宽之前就先失败。
	timeout := updateDownloadTimeoutOrDefault(downloadTimeout)
	manifestData, err := fetchURLBytes(manifestAsset.BrowserDownloadURL, timeout)
	if err != nil {
		return "", fmt.Errorf("download checksum manifest: %w", err)
	}
	expectedSum, err := parseChecksumManifest(manifestData, assetName)
	if err != nil {
		return "", fmt.Errorf("parse checksum manifest: %w", err)
	}

	// 先把压缩包整个缓冲进内存，才能在任何东西落盘前校验完整 SHA-256。发布包大约
	// 10–30 MB；解压代码本来就要把 zip 整个缓冲（随机访问要求），所以 Windows 上
	// 并没有新增内存成本。tar.gz 则多一份内存拷贝——这比「未经 SHA-256 校验就把
	// 不可信字节送进 gzip+tar 解压」安全得多。
	archiveData, err := fetchURLBytes(downloadURL, timeout)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}

	if err := verifyAssetSHA256(archiveData, expectedSum, assetName); err != nil {
		// 不要解压也不要替换；下一个轮询周期会重试。损坏资产极少见，同一 CDN 重试
		// 是正确的默认；持续失败会暴露在 daemon 日志里。
		return "", fmt.Errorf("verify download: %w", err)
	}

	// 从压缩包里取出二进制。
	binaryName := "multica"
	if runtime.GOOS == "windows" {
		binaryName = "multica.exe"
	}
	var binaryData []byte
	if runtime.GOOS == "windows" {
		binaryData, err = extractBinaryFromZip(bytes.NewReader(archiveData), binaryName)
	} else {
		binaryData, err = extractBinaryFromTarGz(bytes.NewReader(archiveData), binaryName)
	}
	if err != nil {
		return "", fmt.Errorf("extract binary: %w", err)
	}

	// 原子替换：先写临时文件，再 rename 覆盖原文件。
	dir := filepath.Dir(exePath)
	tmpFile, err := os.CreateTemp(dir, "multica-update-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(binaryData); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write temp file: %w", err)
	}
	tmpFile.Close()

	// 保留原文件权限。
	info, err := os.Stat(exePath)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("stat original binary: %w", err)
	}
	if err := os.Chmod(tmpPath, info.Mode()); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("chmod temp file: %w", err)
	}

	// 替换原二进制。Windows 上会先把运行中的可执行文件挪到一边；Unix 直接对运行中
	// 的 inode rename 覆盖即可。
	if err := replaceBinary(tmpPath, exePath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("replace binary: %w", err)
	}

	return fmt.Sprintf("Downloaded %s and replaced %s", assetName, exePath), nil
}

// extractBinaryFromTarGz 读 .tar.gz 流并返回其中名为 name 的文件的完整内容。
func extractBinaryFromTarGz(r io.Reader, name string) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("binary %q not found in archive", name)
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		// 按文件名匹配（可能带目录前缀）。
		if filepath.Base(hdr.Name) == name && hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read binary: %w", err)
			}
			return data, nil
		}
	}
}

// extractBinaryFromZip 读 .zip 流并返回其中名为 name 的文件内容。zip 格式要求随机
// 访问，所以整个压缩包必须先缓冲进内存。
func extractBinaryFromZip(r io.Reader, name string) ([]byte, error) {
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read zip data: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("zip reader: %w", err)
	}

	for _, f := range zr.File {
		if filepath.Base(f.Name) == name && !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("open zip entry: %w", err)
			}
			defer rc.Close()

			data, err := io.ReadAll(rc)
			if err != nil {
				return nil, fmt.Errorf("read binary: %w", err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}
