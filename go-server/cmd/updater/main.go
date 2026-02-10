package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Provider describes how to query a provider's model listing API.
type Provider struct {
	URL        string
	AuthEnv    string
	AuthHeader string // empty means use query param auth (Google)
}

var providers = map[string]Provider{
	"OpenAI":    {URL: "https://api.openai.com/v1/models", AuthEnv: "OPENAI_API_KEY", AuthHeader: "Authorization"},
	"Anthropic": {URL: "https://api.anthropic.com/v1/models?limit=1000", AuthEnv: "ANTHROPIC_API_KEY", AuthHeader: "x-api-key"},
	"Google":    {URL: "https://generativelanguage.googleapis.com/v1beta/models", AuthEnv: "GEMINI_API_KEY", AuthHeader: ""},
	"Mistral":   {URL: "https://api.mistral.ai/v1/models", AuthEnv: "MISTRAL_API_KEY", AuthHeader: "Authorization"},
	"xAI":       {URL: "https://api.x.ai/v1/models", AuthEnv: "XAI_API_KEY", AuthHeader: "Authorization"},
	"DeepSeek":  {URL: "https://api.deepseek.com/models", AuthEnv: "DEEPSEEK_API_KEY", AuthHeader: "Authorization"},
}

// knownModels maps provider -> set of model IDs we track in the registry.
var knownModels = map[string]map[string]bool{
	"OpenAI": {
		"gpt-5.2":        true,
		"gpt-5.2-codex":  true,
		"gpt-5.2-pro":    true,
		"gpt-5.1":        true,
		"gpt-5":          true,
		"gpt-5-mini":     true,
		"gpt-5-nano":     true,
		"gpt-4.1-mini":   true,
		"gpt-4.1-nano":   true,
		"o3":             true,
		"o3-pro":         true,
		"o4-mini":        true,
		"o3-mini":        true,
		"gpt-4.1":        true,
		"gpt-4o":         true,
		"gpt-4o-mini":    true,
	},
	"Anthropic": {
		"claude-opus-4-6":              true,
		"claude-sonnet-4-5-20250929":   true,
		"claude-haiku-4-5-20251001":    true,
		"claude-opus-4-5":              true,
		"claude-opus-4-1":              true,
		"claude-sonnet-4-0":            true,
		"claude-3-7-sonnet-20250219":   true,
		"claude-opus-4-0":              true,
	},
	"Google": {
		"gemini-3-pro-preview":   true,
		"gemini-3-flash-preview": true,
		"gemini-2.5-pro":         true,
		"gemini-2.5-flash":       true,
		"gemini-2.5-flash-lite":  true,
		"gemini-2.0-flash":       true,
	},
	"xAI": {
		"grok-4":           true,
		"grok-4.1-fast":    true,
		"grok-4-fast":      true,
		"grok-code-fast-1": true,
		"grok-3":           true,
		"grok-3-mini":      true,
	},
	"Mistral": {
		"mistral-large-2512":  true,
		"mistral-medium-2505": true,
		"mistral-small-2506":  true,
		"devstral-2512":       true,
		"devstral-small-2512": true,
		"codestral-2508":      true,
	},
	"DeepSeek": {
		"deepseek-reasoner": true,
		"deepseek-chat":     true,
		"deepseek-r1":       true,
		"deepseek-v3":       true,
	},
	"Meta": {
		"llama-4-maverick": true,
		"llama-4-scout":    true,
		"llama-3.3-70b":    true,
	},
	"Amazon": {
		"amazon-nova-micro":     true,
		"amazon-nova-lite":      true,
		"amazon-nova-pro":       true,
		"amazon-nova-premier":   true,
		"amazon-nova-2-lite":    true,
		"amazon-nova-2-pro":     true,
	},
	"Cohere": {
		"command-a-03-2025":            true,
		"command-a-reasoning-08-2025":  true,
		"command-a-vision-07-2025":     true,
		"command-r7b-12-2024":          true,
	},
	"Perplexity": {
		"sonar":                true,
		"sonar-pro":            true,
		"sonar-reasoning-pro":  true,
	},
	"AI21": {
		"jamba-large-1.7": true,
		"jamba-mini-1.7":  true,
	},
}

// apiResponse is the common shape returned by OpenAI-compatible model list APIs.
type apiResponse struct {
	Data   []apiModel `json:"data"`
	Models []apiModel `json:"models"` // Google uses top-level "models" array
}

type apiModel struct {
	ID   string `json:"id"`
	Name string `json:"name"` // Google uses "name" (e.g. "models/gemini-2.5-pro")
}

const maxRetries = 3

func main() {
	client := &http.Client{Timeout: 30 * time.Second}
	ctx := context.Background()

	hasChanges := false
	hasErrors := false
	providerOrder := []string{"OpenAI", "Anthropic", "Google", "Mistral", "xAI", "DeepSeek"}

	// Capture report output for GitHub issue creation.
	var report strings.Builder
	// Collect all missing model IDs for auto-deprecation PR.
	var allMissing []string
	// Collect all new model IDs for issue reporting.
	var allNew []string

	logf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		fmt.Print(line)
		report.WriteString(line)
	}

	logf("=== Model Registry Update Check ===\n")
	logf("Time: %s\n\n", time.Now().UTC().Format(time.RFC3339))

	for _, name := range providerOrder {
		p := providers[name]
		key := os.Getenv(p.AuthEnv)
		if key == "" {
			logf("[%s] SKIP: %s not set\n", name, p.AuthEnv)
			continue
		}

		ids, err := fetchModelsWithRetry(ctx, client, name, p, key)
		if err != nil {
			logf("[%s] ERROR: %v\n", name, err)
			hasErrors = true
			continue
		}

		known := knownModels[name]
		newModels, missing := diff(known, ids)

		logf("[%s] API returned %d models, we track %d\n", name, len(ids), len(known))

		if len(newModels) > 0 {
			hasChanges = true
			sort.Strings(newModels)
			allNew = append(allNew, newModels...)
			logf("  NEW (%d):\n", len(newModels))
			for _, m := range newModels {
				logf("    + %s\n", m)
			}
		}
		if len(missing) > 0 {
			hasChanges = true
			sort.Strings(missing)
			allMissing = append(allMissing, missing...)
			logf("  MISSING from API (%d):\n", len(missing))
			for _, m := range missing {
				logf("    - %s\n", m)
			}
		}
		if len(newModels) == 0 && len(missing) == 0 {
			logf("  OK: in sync\n")
		}
		logf("\n")
	}

	// Providers without direct model-listing APIs — just note them.
	logf("[Meta] SKIP: no direct API (models are provider-hosted)\n")
	logf("[Amazon] SKIP: no public model-listing API (check AWS Bedrock console)\n")
	logf("[Cohere] SKIP: no public model-listing API (check docs.cohere.com)\n")
	logf("[Perplexity] SKIP: no public model-listing API (check docs.perplexity.ai)\n")
	logf("[AI21] SKIP: no public model-listing API (check docs.ai21.com)\n")

	logf("\n=== Summary ===\n")
	if hasChanges {
		if hasErrors {
			logf("WARNING: Some providers failed to respond (see errors above).\n")
		}
		logf("Changes detected. Review the output above.\n")
		// Auto-deprecate missing models via PR (fully automatic).
		if len(allMissing) > 0 {
			createDeprecationPR(ctx, client, allMissing, report.String())
		}
		// New models need human review — create an issue.
		if len(allNew) > 0 {
			createGitHubIssue(ctx, client, report.String())
		}
		os.Exit(1)
	} else if hasErrors {
		logf("No model changes detected, but some providers could not be checked.\n")
		os.Exit(1)
	}
	logf("All tracked providers are in sync.\n")
	os.Exit(0)
}

// fetchModelsWithRetry wraps fetchModels with retry logic for transient failures.
func fetchModelsWithRetry(ctx context.Context, client *http.Client, name string, p Provider, key string) ([]string, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		ids, err := fetchModels(ctx, client, name, p, key)
		if err == nil {
			return ids, nil
		}
		lastErr = err
		if attempt < maxRetries {
			backoff := time.Duration(attempt) * 2 * time.Second
			fmt.Printf("[%s] attempt %d/%d failed: %v (retrying in %s)\n", name, attempt, maxRetries, err, backoff)
			time.Sleep(backoff)
		}
	}
	return nil, fmt.Errorf("all %d attempts failed: %w", maxRetries, lastErr)
}

// fetchModels queries a provider's model listing endpoint and returns model IDs.
func fetchModels(ctx context.Context, client *http.Client, name string, p Provider, key string) ([]string, error) {
	url := p.URL
	if p.AuthHeader == "" {
		// Google: API key as query parameter, request large page to avoid pagination
		url += "?key=" + key + "&pageSize=100"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	if p.AuthHeader != "" {
		if p.AuthHeader == "x-api-key" {
			// Anthropic uses x-api-key header + version header
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			// OpenAI-style Bearer auth
			req.Header.Set(p.AuthHeader, "Bearer "+key)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	// Collect model IDs from whichever field is populated.
	seen := make(map[string]bool)
	var ids []string
	models := result.Data
	if len(models) == 0 {
		models = result.Models
	}
	for _, m := range models {
		id := m.ID
		if id == "" && m.Name != "" {
			// Google returns "models/gemini-2.5-pro" — strip prefix.
			id = strings.TrimPrefix(m.Name, "models/")
		}
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("API returned 0 models (possible format change)")
	}

	return ids, nil
}

// createGitHubIssue creates a GitHub issue with the update report.
// Requires GITHUB_TOKEN and GITHUB_REPO (e.g. "owner/repo") environment variables.
// If either is unset, it silently skips (allowing standalone CLI usage).
func createGitHubIssue(ctx context.Context, client *http.Client, reportBody string) {
	token := os.Getenv("GITHUB_TOKEN")
	repo := os.Getenv("GITHUB_REPO")
	if token == "" || repo == "" {
		return
	}

	today := time.Now().Format("2006-01-02")
	title := "Model Update Detected - " + today

	// Check for existing open issue with the same title to avoid duplicates.
	searchURL := fmt.Sprintf("https://api.github.com/search/issues?q=%s+repo:%s+state:open+label:auto-update",
		strings.ReplaceAll(title, " ", "+"), repo)
	searchReq, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		fmt.Printf("[GitHub] failed to create search request: %v\n", err)
		return
	}
	searchReq.Header.Set("Authorization", "Bearer "+token)
	searchReq.Header.Set("Accept", "application/vnd.github+json")

	searchResp, err := client.Do(searchReq)
	if err != nil {
		fmt.Printf("[GitHub] failed to search issues: %v\n", err)
		return
	}
	defer searchResp.Body.Close()

	if searchResp.StatusCode == http.StatusOK {
		var searchResult struct {
			TotalCount int `json:"total_count"`
		}
		if err := json.NewDecoder(searchResp.Body).Decode(&searchResult); err == nil && searchResult.TotalCount > 0 {
			fmt.Printf("[GitHub] Issue already exists for today, skipping.\n")
			return
		}
	}

	// Create the issue.
	issueURL := fmt.Sprintf("https://api.github.com/repos/%s/issues", repo)
	body := map[string]any{
		"title":  title,
		"body":   "```\n" + reportBody + "\n```",
		"labels": []string{"auto-update"},
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		fmt.Printf("[GitHub] failed to marshal issue body: %v\n", err)
		return
	}

	issueReq, err := http.NewRequestWithContext(ctx, http.MethodPost, issueURL, bytes.NewReader(bodyJSON))
	if err != nil {
		fmt.Printf("[GitHub] failed to create issue request: %v\n", err)
		return
	}
	issueReq.Header.Set("Authorization", "Bearer "+token)
	issueReq.Header.Set("Accept", "application/vnd.github+json")
	issueReq.Header.Set("Content-Type", "application/json")

	issueResp, err := client.Do(issueReq)
	if err != nil {
		fmt.Printf("[GitHub] failed to create issue: %v\n", err)
		return
	}
	defer issueResp.Body.Close()

	if issueResp.StatusCode == http.StatusCreated {
		var created struct {
			HTMLURL string `json:"html_url"`
		}
		json.NewDecoder(issueResp.Body).Decode(&created)
		fmt.Printf("[GitHub] Issue created: %s\n", created.HTMLURL)
	} else {
		respBody, _ := io.ReadAll(io.LimitReader(issueResp.Body, 512))
		fmt.Printf("[GitHub] Failed to create issue (HTTP %d): %s\n", issueResp.StatusCode, string(respBody))
	}
}

// createDeprecationPR creates a GitHub PR that changes the status of missing models
// to "deprecated" in data.go. Uses the GitHub Contents API — no git clone needed.
// Requires GITHUB_TOKEN and GITHUB_REPO environment variables.
func createDeprecationPR(ctx context.Context, client *http.Client, missingIDs []string, reportBody string) {
	token := os.Getenv("GITHUB_TOKEN")
	repo := os.Getenv("GITHUB_REPO")
	if token == "" || repo == "" {
		return
	}

	apiBase := "https://api.github.com"
	filePath := "go-server/internal/models/data.go"
	today := time.Now().Format("2006-01-02")
	branchName := "auto-deprecate-" + today

	doReq := func(method, url string, body any) (*http.Response, error) {
		var reader io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			reader = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return client.Do(req)
	}

	// Step 1: Get current data.go content and blob SHA.
	fileURL := fmt.Sprintf("%s/repos/%s/contents/%s", apiBase, repo, filePath)
	resp, err := doReq(http.MethodGet, fileURL, nil)
	if err != nil {
		fmt.Printf("[GitHub PR] failed to get file: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("[GitHub PR] failed to get file: HTTP %d\n", resp.StatusCode)
		return
	}

	var fileInfo struct {
		SHA     string `json:"sha"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fileInfo); err != nil {
		fmt.Printf("[GitHub PR] failed to decode file info: %v\n", err)
		return
	}

	// Decode base64 content (GitHub inserts newlines in base64).
	rawContent, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(fileInfo.Content, "\n", ""))
	if err != nil {
		fmt.Printf("[GitHub PR] failed to decode file content: %v\n", err)
		return
	}

	// Step 2: Apply deprecation changes to the file content.
	content := string(rawContent)
	changed := false
	for _, id := range missingIDs {
		// Match the Status line for this model's block. The pattern matches:
		//   "model-id": {  ...  Status: "current",  or  Status: "legacy",
		// and replaces with Status: "deprecated".
		// We use a targeted regex that finds the model block by ID.
		pattern := fmt.Sprintf(`("%s":\s*\{[^}]*Status:\s*)"(?:current|legacy)"`, regexp.QuoteMeta(id))
		re := regexp.MustCompile(pattern)
		if re.MatchString(content) {
			content = re.ReplaceAllString(content, `${1}"deprecated"`)
			changed = true
			fmt.Printf("[GitHub PR] Marking %s as deprecated\n", id)
		}
	}

	if !changed {
		fmt.Printf("[GitHub PR] No status changes needed in data.go\n")
		return
	}

	// Step 3: Get main branch SHA to create branch from.
	refURL := fmt.Sprintf("%s/repos/%s/git/ref/heads/main", apiBase, repo)
	resp, err = doReq(http.MethodGet, refURL, nil)
	if err != nil {
		fmt.Printf("[GitHub PR] failed to get main ref: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("[GitHub PR] failed to get main ref: HTTP %d\n", resp.StatusCode)
		return
	}

	var refInfo struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refInfo); err != nil {
		fmt.Printf("[GitHub PR] failed to decode ref info: %v\n", err)
		return
	}

	// Step 4: Create new branch.
	createRefURL := fmt.Sprintf("%s/repos/%s/git/refs", apiBase, repo)
	resp, err = doReq(http.MethodPost, createRefURL, map[string]string{
		"ref": "refs/heads/" + branchName,
		"sha": refInfo.Object.SHA,
	})
	if err != nil {
		fmt.Printf("[GitHub PR] failed to create branch: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Printf("[GitHub PR] failed to create branch (HTTP %d): %s\n", resp.StatusCode, string(body))
		return
	}

	// Step 5: Update file on new branch.
	sort.Strings(missingIDs)
	commitMsg := fmt.Sprintf("auto: deprecate %s (removed from provider API)", strings.Join(missingIDs, ", "))
	resp, err = doReq(http.MethodPut, fileURL, map[string]string{
		"message": commitMsg,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"sha":     fileInfo.SHA,
		"branch":  branchName,
	})
	if err != nil {
		fmt.Printf("[GitHub PR] failed to update file: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Printf("[GitHub PR] failed to update file (HTTP %d): %s\n", resp.StatusCode, string(body))
		return
	}

	// Step 6: Create pull request.
	prURL := fmt.Sprintf("%s/repos/%s/pulls", apiBase, repo)
	prBody := fmt.Sprintf("## Auto-Deprecation\n\nModels removed from provider APIs:\n")
	for _, id := range missingIDs {
		prBody += fmt.Sprintf("- `%s`\n", id)
	}
	prBody += fmt.Sprintf("\n<details>\n<summary>Full update report</summary>\n\n```\n%s\n```\n</details>", reportBody)

	resp, err = doReq(http.MethodPost, prURL, map[string]any{
		"title": "auto: deprecate models removed from provider APIs — " + today,
		"body":  prBody,
		"head":  branchName,
		"base":  "main",
	})
	if err != nil {
		fmt.Printf("[GitHub PR] failed to create PR: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		var pr struct {
			HTMLURL string `json:"html_url"`
			Number  int    `json:"number"`
		}
		json.NewDecoder(resp.Body).Decode(&pr)
		fmt.Printf("[GitHub PR] Created: %s\n", pr.HTMLURL)
		// Add auto-update label to the PR for auto-merge workflow.
		labelURL := fmt.Sprintf("%s/repos/%s/issues/%d/labels", apiBase, repo, pr.Number)
		resp, err = doReq(http.MethodPost, labelURL, []string{"auto-update"})
		if err == nil {
			defer resp.Body.Close()
		}
	} else {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Printf("[GitHub PR] Failed to create PR (HTTP %d): %s\n", resp.StatusCode, string(body))
	}
}

// diff compares our known models against API results.
// Returns new models (in API but not known) and missing models (known but not in API).
func diff(known map[string]bool, apiIDs []string) (newModels, missing []string) {
	apiSet := make(map[string]bool, len(apiIDs))
	for _, id := range apiIDs {
		apiSet[id] = true
	}

	// New: in API but not in our registry
	for _, id := range apiIDs {
		if !known[id] {
			newModels = append(newModels, id)
		}
	}

	// Missing: in our registry but not in API
	for id := range known {
		if !apiSet[id] {
			missing = append(missing, id)
		}
	}

	return newModels, missing
}
