package download

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Issue is the normalized schema written to issues.jsonl.
type Issue struct {
	Repo          string    `json:"repo"`
	Number        int       `json:"number"`
	Title         string    `json:"title"`
	Labels        []string  `json:"labels"`
	CreatedAt     string    `json:"created_at"`
	UpdatedAt     string    `json:"updated_at"`
	CommentsCount int       `json:"comments_count"`
	URL           string    `json:"url"`
	Body          string    `json:"body"`
	Comments      []Comment `json:"comments"`
}

type Comment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type Summary struct {
	TotalIssues  int            `json:"total_issues"`
	ByRepo       map[string]int `json:"by_repo"`
	Fetched      int            `json:"fetched"`
	Cached       int            `json:"cached"`
	Errors       int            `json:"errors"`
	DownloadedAt string         `json:"downloaded_at"`
}

type cacheEntry struct {
	UpdatedAt string `json:"updated_at"`
	Path      string `json:"path"`
}

// Run downloads issues from the given repos and writes issues.jsonl + download-summary.json.
func Run(repos []string, state string, outputDir string, cacheDir string, workers int) error {
	os.MkdirAll(outputDir, 0755)
	os.MkdirAll(cacheDir, 0755)

	cacheIndex := loadCacheIndex(cacheDir)

	var allIssues []Issue
	byRepo := make(map[string]int)
	var totalFetched, totalCached, totalErrors int

	for _, repo := range repos {
		issues, fetched, cached, errors, updates := processRepo(repo, state, cacheDir, cacheIndex, workers)
		allIssues = append(allIssues, issues...)
		byRepo[repo] = len(issues)
		totalFetched += fetched
		totalCached += cached
		totalErrors += errors
		for k, v := range updates {
			cacheIndex[k] = v
		}
	}

	saveCacheIndex(cacheDir, cacheIndex)

	jsonlPath := filepath.Join(outputDir, "issues.jsonl")
	if err := writeJSONL(allIssues, jsonlPath); err != nil {
		return fmt.Errorf("writing issues.jsonl: %w", err)
	}

	summary := Summary{
		TotalIssues:  len(allIssues),
		ByRepo:       byRepo,
		Fetched:      totalFetched,
		Cached:       totalCached,
		Errors:       totalErrors,
		DownloadedAt: time.Now().UTC().Format(time.RFC3339),
	}
	summaryPath := filepath.Join(outputDir, "download-summary.json")
	if err := writeJSON(summary, summaryPath); err != nil {
		return fmt.Errorf("writing summary: %w", err)
	}

	parts := make([]string, 0, len(repos))
	for _, r := range repos {
		short := r[strings.LastIndex(r, "/")+1:]
		parts = append(parts, fmt.Sprintf("%d %s", byRepo[r], short))
	}
	fmt.Printf("\n%d issues (%s)\n", len(allIssues), strings.Join(parts, ", "))
	fmt.Printf("%d fetched, %d cached, %d errors\n", totalFetched, totalCached, totalErrors)

	return nil
}

func processRepo(repo, state, cacheDir string, cacheIndex map[string]cacheEntry, workers int) ([]Issue, int, int, int, map[string]cacheEntry) {
	slug := strings.ReplaceAll(repo, "/", "-")
	short := repo[strings.LastIndex(repo, "/")+1:]

	rawIssues, err := fetchIssueList(repo, state)
	if err != nil {
		slog.Error("fetching issue list", "repo", repo, "error", err)
		return nil, 0, 0, 1, nil
	}

	type fetchItem struct {
		raw      ghIssue
		cacheKey string
	}

	var issues []Issue
	var toFetch []fetchItem
	cached := 0

	for _, raw := range rawIssues {
		key := fmt.Sprintf("%s/%d", slug, raw.Number)
		entry, ok := cacheIndex[key]
		if ok && entry.UpdatedAt == raw.UpdatedAt {
			if issue := loadCachedIssue(cacheDir, key); issue != nil {
				issues = append(issues, *issue)
				cached++
				continue
			}
		}
		toFetch = append(toFetch, fetchItem{raw: raw, cacheKey: key})
	}

	if len(toFetch) > 0 {
		var mu sync.Mutex
		var errCount int64
		updates := make(map[string]cacheEntry)
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup

		for _, item := range toFetch {
			wg.Add(1)
			go func(it fetchItem) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				comments, err := fetchComments(repo, it.raw.Number)
				if err != nil {
					slog.Error("fetching comments", "issue", it.cacheKey, "error", err)
					atomic.AddInt64(&errCount, 1)
					return
				}
				issue := normalize(it.raw, comments, repo)
				saveCachedIssue(cacheDir, it.cacheKey, &issue)

				mu.Lock()
				issues = append(issues, issue)
				updates[it.cacheKey] = cacheEntry{UpdatedAt: it.raw.UpdatedAt, Path: fmt.Sprintf("issues/%s.json", it.cacheKey)}
				mu.Unlock()
			}(item)
		}
		wg.Wait()

		fetched := len(toFetch) - int(errCount)
		slog.Info("repo processed", "repo", short, "fetched", fetched, "cached", cached)
		return issues, fetched, cached, int(errCount), updates
	}

	slog.Info("repo processed", "repo", short, "fetched", 0, "cached", cached)
	return issues, 0, cached, 0, nil
}

// gh CLI types

type ghLabel struct {
	Name string `json:"name"`
}

type ghIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Labels    []ghLabel `json:"labels"`
	CreatedAt string    `json:"createdAt"`
	UpdatedAt string    `json:"updatedAt"`
	URL       string    `json:"url"`
	Body      string    `json:"body"`
}

type ghComment struct {
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

func fetchIssueList(repo, state string) ([]ghIssue, error) {
	out, err := exec.Command("gh", "issue", "list",
		"--repo", repo,
		"--state", state,
		"--limit", "5000",
		"--json", "number,title,labels,createdAt,updatedAt,url,body",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue list: %w", err)
	}
	var issues []ghIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing issue list: %w", err)
	}
	return issues, nil
}

func fetchComments(repo string, number int) ([]ghComment, error) {
	parts := strings.SplitN(repo, "/", 2)
	out, err := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/%s/issues/%d/comments", parts[0], parts[1], number),
		"--paginate",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh api comments: %w", err)
	}
	var comments []ghComment
	if err := json.Unmarshal(out, &comments); err != nil {
		return nil, fmt.Errorf("parsing comments: %w", err)
	}
	return comments, nil
}

func normalize(raw ghIssue, comments []ghComment, repo string) Issue {
	labels := make([]string, len(raw.Labels))
	for i, l := range raw.Labels {
		labels[i] = l.Name
	}
	normalized := make([]Comment, len(comments))
	for i, c := range comments {
		normalized[i] = Comment{
			Author:    c.User.Login,
			Body:      c.Body,
			CreatedAt: c.CreatedAt,
		}
	}
	return Issue{
		Repo:          repo,
		Number:        raw.Number,
		Title:         raw.Title,
		Labels:        labels,
		CreatedAt:     raw.CreatedAt,
		UpdatedAt:     raw.UpdatedAt,
		CommentsCount: len(comments),
		URL:           raw.URL,
		Body:          raw.Body,
		Comments:      normalized,
	}
}

// Cache helpers

func loadCacheIndex(cacheDir string) map[string]cacheEntry {
	path := filepath.Join(cacheDir, "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]cacheEntry)
	}
	var index map[string]cacheEntry
	if json.Unmarshal(data, &index) != nil {
		return make(map[string]cacheEntry)
	}
	return index
}

func saveCacheIndex(cacheDir string, index map[string]cacheEntry) {
	path := filepath.Join(cacheDir, "index.json")
	data, _ := json.MarshalIndent(index, "", "  ")
	os.WriteFile(path, data, 0644)
}

func loadCachedIssue(cacheDir, key string) *Issue {
	path := filepath.Join(cacheDir, "issues", key+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var issue Issue
	if json.Unmarshal(data, &issue) != nil {
		return nil
	}
	return &issue
}

func saveCachedIssue(cacheDir, key string, issue *Issue) {
	dir := filepath.Join(cacheDir, "issues", filepath.Dir(key))
	os.MkdirAll(dir, 0755)
	path := filepath.Join(cacheDir, "issues", key+".json")
	data, _ := json.Marshal(issue)
	os.WriteFile(path, data, 0644)
}

// File helpers

func writeJSONL(issues []Issue, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, issue := range issues {
		if err := enc.Encode(issue); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(v any, path string) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}
