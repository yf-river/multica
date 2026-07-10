package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

)

func parseClawHubSlug(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	// /{owner}/{slug} — take the last segment as the slug
	if len(parts) == 2 {
		return parts[1], nil
	}
	if len(parts) == 1 && parts[0] != "" {
		return parts[0], nil
	}
	// Bare slug (no path)
	if raw == parsed.Host || parsed.Path == "" || parsed.Path == "/" {
		return "", fmt.Errorf("missing skill slug in URL")
	}
	return "", fmt.Errorf("could not extract skill slug from URL: %s", raw)
}

func searchClawHubSkills(httpClient *http.Client, query string) ([]SkillSearchCandidateResponse, error) {
	searchURL := clawHubAPIBase + "/search?q=" + url.QueryEscape(query)
	resp, err := httpClient.Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("failed to reach ClawHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ClawHub search returned status %d", resp.StatusCode)
	}

	var searchResp clawhubSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("failed to parse ClawHub search response")
	}

	candidates := make([]SkillSearchCandidateResponse, 0, len(searchResp.Results))
	for i, result := range searchResp.Results {
		if result.Slug == "" {
			continue
		}
		candidate := SkillSearchCandidateResponse{
			Name:        result.DisplayName,
			URL:         buildClawHubSkillURL(result.OwnerHandle, result.Slug),
			Source:      "clawhub.ai",
			Description: result.Summary,
		}
		if candidate.Name == "" {
			candidate.Name = result.Slug
		}
		if i < clawHubSearchStatsLimit {
			if count, ok := fetchClawHubInstallCount(httpClient, result.Slug); ok {
				candidate.InstallCount = &count
			}
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func buildClawHubSkillURL(ownerHandle, slug string) string {
	if ownerHandle == "" {
		return "https://clawhub.ai/" + url.PathEscape(slug)
	}
	return "https://clawhub.ai/" + url.PathEscape(ownerHandle) + "/" + url.PathEscape(slug)
}

func fetchClawHubInstallCount(httpClient *http.Client, slug string) (int64, bool) {
	detailURL := clawHubAPIBase + "/skills/" + url.PathEscape(slug)
	resp, err := httpClient.Get(detailURL)
	if err != nil {
		slog.Warn("clawhub search: failed to fetch skill details", "slug", slug, "error", err)
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("clawhub search: skill details returned non-200", "slug", slug, "status", resp.StatusCode)
		return 0, false
	}
	var detail clawhubGetSkillResponse
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		slog.Warn("clawhub search: failed to parse skill details", "slug", slug, "error", err)
		return 0, false
	}
	if detail.Skill.Stats.InstallsAllTime > 0 {
		return detail.Skill.Stats.InstallsAllTime, true
	}
	return detail.Skill.Stats.InstallsCurrent, true
}

func fetchFromClawHub(httpClient *http.Client, rawURL string) (*importedSkill, error) {
	slug, err := parseClawHubSlug(rawURL)
	if err != nil {
		return nil, err
	}

	apiBase := clawHubAPIBase

	// 1. Fetch skill metadata
	skillResp, err := httpClient.Get(apiBase + "/skills/" + url.PathEscape(slug))
	if err != nil {
		return nil, fmt.Errorf("failed to reach ClawHub: %w", err)
	}
	defer skillResp.Body.Close()

	if skillResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("skill not found on ClawHub: %s", slug)
	}
	if skillResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ClawHub returned status %d", skillResp.StatusCode)
	}

	var chResp clawhubGetSkillResponse
	if err := json.NewDecoder(skillResp.Body).Decode(&chResp); err != nil {
		return nil, fmt.Errorf("failed to parse ClawHub response")
	}
	chSkill := chResp.Skill

	// 2. Determine latest version and fetch file list
	latestVersion := ""
	if v, ok := chSkill.Tags["latest"]; ok {
		latestVersion = v
	} else if chResp.LatestVersion != nil {
		latestVersion = chResp.LatestVersion.Version
	}

	var filePaths []string
	if latestVersion != "" {
		vURL := fmt.Sprintf("%s/skills/%s/versions/%s", apiBase, url.PathEscape(slug), url.PathEscape(latestVersion))
		vResp, err := httpClient.Get(vURL)
		if err == nil {
			defer vResp.Body.Close()
			if vResp.StatusCode == http.StatusOK {
				var vDetail clawhubVersionDetailResponse
				if err := json.NewDecoder(vResp.Body).Decode(&vDetail); err == nil {
					for _, f := range vDetail.Version.Files {
						filePaths = append(filePaths, f.Path)
					}
				}
			}
		}
	}

	// 3. Download each file
	result := &importedSkill{
		name:        chSkill.DisplayName,
		description: chSkill.Summary,
		origin: map[string]any{
			"type":       "clawhub",
			"source_url": rawURL,
			"slug":       slug,
		},
	}
	if result.name == "" {
		result.name = slug
	}

	for _, fp := range filePaths {
		fileURL := fmt.Sprintf("%s/skills/%s/file?path=%s", apiBase, url.PathEscape(slug), url.QueryEscape(fp))
		if latestVersion != "" {
			fileURL += "&version=" + url.QueryEscape(latestVersion)
		}
		body, err := fetchRawFile(httpClient, fileURL)
		if err != nil {
			// Cap violations must abort: silently dropping a file would
			// produce an incomplete bundle that looks valid. SKILL.md is
			// load-bearing, so any failure on it is fatal too.
			if isCapError(err) || fp == "SKILL.md" {
				return nil, fmt.Errorf("clawhub import: %s: %w", fp, err)
			}
			slog.Warn("clawhub import: file download failed", "path", fp, "error", err)
			continue
		}
		if fp == "SKILL.md" {
			result.content = string(body)
			continue
		}
		if err := result.addFile(fp, string(body)); err != nil {
			return nil, err
		}
	}

	if result.content == "" {
		return nil, fmt.Errorf("clawhub import: SKILL.md is empty or missing for %s", slug)
	}

	return result, nil
}

// --- skills.sh import ---

// parseSkillsShParts extracts owner, repo, skill-name from a skills.sh URL.
// URL format: https://skills.sh/{owner}/{repo}/{skill-name}
