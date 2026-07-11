package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

func validImportOnConflict(strategy string) bool {
	switch strategy {
	case "", importOnConflictFail, importOnConflictOverwrite, importOnConflictRename, importOnConflictSkip:
		return true
	}
	return false
}

// Per-import bundle limits. These mirror the local-runtime importer so that
// URL imports cannot smuggle in payloads that the rest of the stack would
// reject. fetchRawFile enforces the per-file cap; importedSkill.addFile
// enforces the bundle-wide caps.
const (
	maxImportFileSize  = 1 << 20 // 1 MiB per file
	maxImportTotalSize = 8 << 20 // 8 MiB per import bundle (sum of supporting files)
	maxImportFileCount = 128     // max number of supporting files
)

// importedSkill holds the data extracted from an external source.
type importedSkill struct {
	name        string
	description string
	content     string // SKILL.md body
	files       []importedFile
	bundleSize  int            // running sum of file content bytes for cap enforcement
	origin      map[string]any // written into skill.config.origin so the UI can show provenance
}

type importedFile struct {
	path    string
	content string
}

// errImportCapExceeded marks an error caused by a per-file or per-bundle cap.
// Such errors must abort the import — silently dropping a file would otherwise
// produce an incomplete skill that looks valid to the user.
var errImportCapExceeded = errors.New("import cap exceeded")

// isCapError reports whether err is (or wraps) errImportCapExceeded.
func isCapError(err error) bool {
	return errors.Is(err, errImportCapExceeded)
}

// addFile appends a supporting file while enforcing the per-bundle caps. It
// returns an error when either the file count or aggregate byte budget would
// be exceeded so the caller fails the import instead of silently truncating.
//
// Binary files (images, fonts, archives) are silently skipped: their bytes
// can't survive a PG TEXT column (SQLSTATE 22021), and they're reference
// assets the agent never reads as text anyway. Logging the skip leaves a
// breadcrumb if a user expected one of these to import.
func (s *importedSkill) addFile(path, content string) error {
	if isLikelyBinaryFilePath(path) {
		slog.Info("skill import: skipping binary file", "path", path, "size", len(content))
		return nil
	}
	if len(s.files) >= maxImportFileCount {
		return fmt.Errorf("%w: import bundle exceeds %d file limit", errImportCapExceeded, maxImportFileCount)
	}
	if s.bundleSize+len(content) > maxImportTotalSize {
		return fmt.Errorf("%w: import bundle exceeds %d byte limit", errImportCapExceeded, maxImportTotalSize)
	}
	s.bundleSize += len(content)
	s.files = append(s.files, importedFile{path: path, content: content})
	return nil
}

// isLikelyBinaryFilePath reports whether the file's extension indicates a
// non-text payload. Conservative blacklist — extensions not on the list
// are assumed text and pass through. `sanitizeNullBytes` (called at PG
// insert time) is the second-line defence against any text file that
// turns out to have stray invalid-UTF-8 bytes.
func isLikelyBinaryFilePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case
		// images
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".ico", ".heic",
		// fonts
		".ttf", ".otf", ".woff", ".woff2", ".eot",
		// archives
		".zip", ".gz", ".tar", ".bz2", ".7z", ".rar",
		// documents (binary office)
		".pdf", ".docx", ".xlsx", ".pptx", ".doc", ".xls", ".ppt",
		// media
		".mp3", ".mp4", ".wav", ".avi", ".mov", ".webm", ".m4a", ".flac",
		// compiled / executable
		".exe", ".dll", ".so", ".dylib", ".class", ".jar", ".wasm",
		// db / cache
		".db", ".sqlite", ".sqlite3", ".pyc":
		return true
	}
	return false
}

// --- ClawHub types ---

var clawHubAPIBase = "https://clawhub.ai/api/v1"

const clawHubSearchStatsLimit = 10

type clawhubSearchResponse struct {
	Results []clawhubSearchResult `json:"results"`
}

type clawhubSearchResult struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"displayName"`
	Summary     string `json:"summary"`
	OwnerHandle string `json:"ownerHandle"`
}

type clawhubSkillStats struct {
	InstallsAllTime int64 `json:"installsAllTime"`
	InstallsCurrent int64 `json:"installsCurrent"`
}

type clawhubGetSkillResponse struct {
	Skill         clawhubSkill          `json:"skill"`
	LatestVersion *clawhubLatestVersion `json:"latestVersion"`
}

type clawhubSkill struct {
	Slug        string            `json:"slug"`
	DisplayName string            `json:"displayName"`
	Summary     string            `json:"summary"`
	Tags        map[string]string `json:"tags"`
	Stats       clawhubSkillStats `json:"stats"`
}

type clawhubLatestVersion struct {
	Version string `json:"version"`
}

type clawhubVersionDetailResponse struct {
	Version clawhubVersionDetail `json:"version"`
}

type clawhubVersionDetail struct {
	Version string             `json:"version"`
	Files   []clawhubFileEntry `json:"files"`
}

type clawhubFileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// --- GitHub types (for skills.sh) ---

type githubContentEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type"` // "file" or "dir"
	URL         string `json:"url"`
	DownloadURL string `json:"download_url"`
}

type githubRepoInfo struct {
	DefaultBranch string `json:"default_branch"`
}

type githubTreeResponse struct {
	Tree      []githubTreeEntry `json:"tree"`
	Truncated bool              `json:"truncated"`
}

type githubTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "blob" or "tree"
}

// fetchGitHubDefaultBranch returns the default branch of a GitHub repository.
// Falls back to "main" if the API call fails.
func fetchGitHubDefaultBranch(httpClient *http.Client, owner, repo string) string {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s",
		url.PathEscape(owner), url.PathEscape(repo))
	resp, err := doGitHubAPIGet(httpClient, apiURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return "main"
	}
	defer func() { _ = resp.Body.Close() }()

	var info githubRepoInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.DefaultBranch == "" {
		return "main"
	}
	return info.DefaultBranch
}

// --- URL detection ---

// importSource identifies where a URL points.
type importSource int

const (
	sourceClawHub importSource = iota
	sourceSkillsSh
	sourceGitHub
)

// detectImportSource determines the source from a URL.
// Returns the source and a normalized URL (with scheme).
func detectImportSource(raw string) (importSource, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, "", fmt.Errorf("empty URL")
	}

	normalized := raw
	if !strings.HasPrefix(normalized, "http://") && !strings.HasPrefix(normalized, "https://") {
		normalized = "https://" + normalized
	}

	parsed, err := url.Parse(normalized)
	if err != nil {
		return 0, "", fmt.Errorf("invalid URL: %w", err)
	}

	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "skills.sh", "www.skills.sh":
		return sourceSkillsSh, normalized, nil
	case "clawhub.ai", "www.clawhub.ai":
		return sourceClawHub, normalized, nil
	case "github.com", "www.github.com":
		return sourceGitHub, normalized, nil
	default:
		// If no host (bare slug), default to clawhub
		if !strings.Contains(raw, "/") || !strings.Contains(raw, ".") {
			return sourceClawHub, raw, nil
		}
		return 0, "", fmt.Errorf("unsupported source: %s (supported: clawhub.ai, skills.sh, github.com)", host)
	}
}

// --- ClawHub import ---

// parseClawHubSlug extracts the skill slug from a clawhub.ai URL.
