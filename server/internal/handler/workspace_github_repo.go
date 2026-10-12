package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// readGitHubRepository uses only installations belonging to the requested workspace.
func (h *Handler) readGitHubRepository(w http.ResponseWriter, r *http.Request, input workspaceRepoRequest, resolve bool) {
	id, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	path := strings.TrimPrefix(repositoryIdentity(input.URL), "github.com/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		writeError(w, 400, "GitHub 地址必须包含组织与仓库名称")
		return
	}
	installs, err := h.Queries.ListGitHubInstallationsByWorkspace(r.Context(), id)
	if err != nil {
		writeError(w, 500, "读取 GitHub 安装失败")
		return
	}
	if len(installs) == 0 {
		writeError(w, 400, "请先连接 GitHub App")
		return
	}
	for _, installation := range installs {
		token, client, err := createRepositoryInstallationToken(r.Context(), installation.InstallationID, true)
		if err != nil {
			continue
		}
		// Revoke each temporary token even when this installation cannot read the repository.
		result, branches, err := func() (workspaceRepoRef, []string, error) {
			defer revokeGitHubInstallationToken(client, token)
			get := func(endpoint string, out any) error {
				req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(githubAPIBase, "/")+endpoint, nil)
				if err != nil {
					return err
				}
				setGitHubAPIHeaders(req, token)
				res, err := client.Do(req)
				if err != nil {
					return err
				}
				defer res.Body.Close()
				if res.StatusCode != 200 {
					return fmt.Errorf("GitHub 返回 HTTP %d", res.StatusCode)
				}
				return json.NewDecoder(io.LimitReader(res.Body, githubAPIResponseLimit)).Decode(out)
			}
			endpoint := "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
			var metadata struct {
				DefaultBranch string `json:"default_branch"`
			}
			if err := get(endpoint, &metadata); err != nil {
				return workspaceRepoRef{}, nil, err
			}
			branches := []string{}
			for page := 1; page <= 100; page++ {
				var batch []struct {
					Name string `json:"name"`
				}
				if err := get(fmt.Sprintf("%s/branches?per_page=100&page=%d", endpoint, page), &batch); err != nil {
					return workspaceRepoRef{}, nil, err
				}
				for _, b := range batch {
					branches = append(branches, b.Name)
				}
				if len(batch) < 100 {
					break
				}
				if page == 100 {
					return workspaceRepoRef{}, nil, fmt.Errorf("分支过多，请缩小仓库范围")
				}
			}
			branch := strings.TrimSpace(input.DefaultBranch)
			if branch == "" {
				branch = metadata.DefaultBranch
			}
			repo := workspaceRepoRef{URL: input.URL, Provider: "github", ProjectPath: path, DefaultBranch: branch, Ref: branch, ConnectionStatus: "credential_backed", TestStatus: "passed", LastTestedAt: time.Now().UTC().Format(time.RFC3339)}
			if resolve {
				if branch == "" {
					return workspaceRepoRef{}, nil, fmt.Errorf("仓库没有可用分支")
				}
				var commit struct {
					SHA string `json:"sha"`
				}
				if err := get(endpoint+"/commits/"+url.PathEscape(branch), &commit); err != nil {
					return workspaceRepoRef{}, nil, err
				}
				repo.HeadCommit = commit.SHA
				repo.SyncStatus = "metadata_read"
			}
			return repo, branches, nil
		}()
		if err != nil {
			continue
		}
		if resolve {
			writeJSON(w, 200, result)
		} else {
			writeJSON(w, 200, workspaceRepoProbeResponse{URL: input.URL, Provider: "github", ProjectPath: path, DefaultBranch: result.DefaultBranch, Branches: branches, ConnectionStatus: "credential_backed", TestStatus: "passed"})
		}
		return
	}
	writeError(w, 502, "GitHub 仓库或分支读取失败，请检查 App 安装权限及 contents 读取权限")
}
