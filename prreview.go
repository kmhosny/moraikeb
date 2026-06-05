package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// --- Database ---

type PRReviewDB struct {
	db *sql.DB
}

type StoredRepo struct {
	Owner     string `json:"owner"`
	Repo      string `json:"repo"`
	LocalPath string `json:"local_path"`
	UpdatedAt string `json:"updated_at"`
}

func NewPRReviewDB(dbPath string) (*PRReviewDB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS repos (
		owner TEXT NOT NULL,
		repo TEXT NOT NULL,
		local_path TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (owner, repo)
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS config (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &PRReviewDB{db: db}, nil
}

func (d *PRReviewDB) Close() error {
	return d.db.Close()
}

func (d *PRReviewDB) GetRepo(owner, repo string) (*StoredRepo, error) {
	var r StoredRepo
	err := d.db.QueryRow(
		"SELECT owner, repo, local_path, updated_at FROM repos WHERE owner = ? AND repo = ?",
		owner, repo,
	).Scan(&r.Owner, &r.Repo, &r.LocalPath, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

func (d *PRReviewDB) SaveRepo(owner, repo, localPath string) error {
	_, err := d.db.Exec(
		`INSERT INTO repos (owner, repo, local_path, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(owner, repo) DO UPDATE SET local_path = excluded.local_path, updated_at = excluded.updated_at`,
		owner, repo, localPath, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (d *PRReviewDB) ListRepos() ([]StoredRepo, error) {
	rows, err := d.db.Query("SELECT owner, repo, local_path, updated_at FROM repos ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []StoredRepo
	for rows.Next() {
		var r StoredRepo
		if err := rows.Scan(&r.Owner, &r.Repo, &r.LocalPath, &r.UpdatedAt); err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, nil
}

// --- Config ---

func (d *PRReviewDB) GetConfig(key string) string {
	var val string
	err := d.db.QueryRow("SELECT value FROM config WHERE key = ?", key).Scan(&val)
	if err != nil {
		return ""
	}
	return val
}

func (d *PRReviewDB) SetConfig(key, value string) error {
	_, err := d.db.Exec(
		`INSERT INTO config (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

func (d *PRReviewDB) GetAllConfig() map[string]string {
	rows, err := d.db.Query("SELECT key, value FROM config")
	if err != nil {
		return map[string]string{}
	}
	defer rows.Close()
	cfg := map[string]string{}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			cfg[k] = v
		}
	}
	return cfg
}

// --- PR URL parsing ---

var prURLPattern = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

type ParsedPR struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

func parsePRURL(url string) (*ParsedPR, error) {
	matches := prURLPattern.FindStringSubmatch(url)
	if matches == nil {
		return nil, fmt.Errorf("invalid GitHub PR URL — expected: https://github.com/owner/repo/pull/123")
	}
	var n int
	fmt.Sscanf(matches[3], "%d", &n)
	return &ParsedPR{Owner: matches[1], Repo: matches[2], Number: n}, nil
}

// --- GitHub integration ---

type PRInfo struct {
	Owner   string   `json:"owner"`
	Repo    string   `json:"repo"`
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Branch  string   `json:"branch"`
	Commits []string `json:"commits"`
}

// fetchPRInfoRouted picks the method based on stored config.
func fetchPRInfoRouted(db *PRReviewDB, owner, repo string, number int) (*PRInfo, error) {
	method := db.GetConfig("github_method")

	switch method {
	case "gh":
		return fetchPRInfoGH(owner, repo, number)
	case "token":
		token := db.GetConfig("github_token")
		if token == "" {
			return nil, fmt.Errorf("GitHub token not configured — open Settings in the PR Review modal to add one")
		}
		return fetchPRInfoAPI(owner, repo, number, token)
	default:
		// Auto-detect: try gh first, fall back to token
		if _, err := exec.LookPath("gh"); err == nil {
			return fetchPRInfoGH(owner, repo, number)
		}
		token := db.GetConfig("github_token")
		if token != "" {
			return fetchPRInfoAPI(owner, repo, number, token)
		}
		return nil, fmt.Errorf("no GitHub method configured — open Settings in the PR Review modal to choose gh CLI or add an API token")
	}
}

// fetchPRInfoGH uses the gh CLI.
func fetchPRInfoGH(owner, repo string, number int) (*PRInfo, error) {
	out, err := exec.Command("gh", "pr", "view", fmt.Sprintf("%d", number),
		"--repo", owner+"/"+repo,
		"--json", "title,body,headRefName,commits").Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh pr view failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("gh CLI not found or failed: %w", err)
	}

	var ghData struct {
		Title       string `json:"title"`
		Body        string `json:"body"`
		HeadRefName string `json:"headRefName"`
		Commits     []struct {
			MessageHeadline string `json:"messageHeadline"`
			Oid             string `json:"oid"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(out, &ghData); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}

	var commits []string
	for _, c := range ghData.Commits {
		short := c.Oid
		if len(short) > 8 {
			short = short[:8]
		}
		commits = append(commits, short+" "+c.MessageHeadline)
	}

	return &PRInfo{
		Owner:   owner,
		Repo:    repo,
		Number:  number,
		Title:   ghData.Title,
		Body:    ghData.Body,
		Branch:  ghData.HeadRefName,
		Commits: commits,
	}, nil
}

// fetchPRInfoAPI uses the GitHub REST API with a personal access token.
func fetchPRInfoAPI(owner, repo string, number int, token string) (*PRInfo, error) {
	client := &http.Client{Timeout: 15 * time.Second}

	// Fetch PR details
	prURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, number)
	prData, err := githubGet(client, prURL, token)
	if err != nil {
		return nil, fmt.Errorf("fetch PR: %w", err)
	}

	title, _ := prData["title"].(string)
	body, _ := prData["body"].(string)
	head, _ := prData["head"].(map[string]interface{})
	branch := ""
	if head != nil {
		branch, _ = head["ref"].(string)
	}

	// Fetch commits
	commitsURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/commits?per_page=100", owner, repo, number)
	commitsResp, err := githubGetArray(client, commitsURL, token)
	if err != nil {
		return nil, fmt.Errorf("fetch commits: %w", err)
	}

	var commits []string
	for _, item := range commitsResp {
		obj, _ := item.(map[string]interface{})
		if obj == nil {
			continue
		}
		sha, _ := obj["sha"].(string)
		if len(sha) > 8 {
			sha = sha[:8]
		}
		commit, _ := obj["commit"].(map[string]interface{})
		msg := ""
		if commit != nil {
			fullMsg, _ := commit["message"].(string)
			// Take first line only
			if idx := strings.IndexByte(fullMsg, '\n'); idx >= 0 {
				msg = fullMsg[:idx]
			} else {
				msg = fullMsg
			}
		}
		commits = append(commits, sha+" "+msg)
	}

	return &PRInfo{
		Owner:   owner,
		Repo:    repo,
		Number:  number,
		Title:   title,
		Body:    body,
		Branch:  branch,
		Commits: commits,
	}, nil
}

func githubGet(client *http.Client, url, token string) (map[string]interface{}, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

func githubGetArray(client *http.Client, url, token string) ([]interface{}, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	var data []interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

// --- Review orchestration ---

func startReview(db *PRReviewDB, repoPath string, pr *PRInfo) (string, error) {
	// Validate git repo
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		return "", fmt.Errorf("not a git repository: %s", repoPath)
	}

	// Prune stale worktrees first
	pruneCmd := exec.Command("git", "worktree", "prune")
	pruneCmd.Dir = repoPath
	pruneCmd.CombinedOutput()

	// Pick a unique branch name — check if base name is already checked out in a worktree
	branchName := fmt.Sprintf("pr-review-%d", pr.Number)
	if branchInWorktree(repoPath, branchName) {
		suffix := fmt.Sprintf("%x", time.Now().UnixNano()%0xFFFF)
		branchName = fmt.Sprintf("pr-review-%d-%s", pr.Number, suffix)
	}

	// Fetch the PR ref into the local branch
	cmd := exec.Command("git", "fetch", "origin",
		fmt.Sprintf("+pull/%d/head:%s", pr.Number, branchName))
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git fetch PR failed: %s: %w", string(out), err)
	}

	// Create worktree at same level as repo root (not inside it)
	repoParent := filepath.Dir(repoPath)
	repoName := filepath.Base(repoPath)
	worktreePath := filepath.Join(repoParent, fmt.Sprintf("%s-%s", repoName, branchName))

	// Remove existing worktree directory if present
	if _, err := os.Stat(worktreePath); err == nil {
		rmCmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		rmCmd.Dir = repoPath
		rmCmd.CombinedOutput() // best effort
	}

	// Add worktree
	cmd = exec.Command("git", "worktree", "add", worktreePath, branchName)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add failed: %s: %w", string(out), err)
	}

	// Build prompt and launch claude
	prompt := buildReviewPrompt(pr)
	if err := launchClaude(db, worktreePath, prompt); err != nil {
		return "", fmt.Errorf("launch claude: %w", err)
	}

	return worktreePath, nil
}

// branchInWorktree checks if a branch is currently checked out in any worktree.
func branchInWorktree(repoPath, branch string) bool {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		// Lines look like: "branch refs/heads/pr-review-10153"
		if strings.HasPrefix(line, "branch refs/heads/") {
			name := strings.TrimPrefix(line, "branch refs/heads/")
			if name == branch {
				return true
			}
		}
	}
	return false
}

func buildReviewPrompt(pr *PRInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are reviewing Pull Request #%d in %s/%s.\n\n", pr.Number, pr.Owner, pr.Repo)
	b.WriteString("## PR Title\n")
	b.WriteString(pr.Title + "\n\n")
	b.WriteString("## PR Description\n")
	if pr.Body != "" {
		b.WriteString(pr.Body + "\n\n")
	} else {
		b.WriteString("(no description provided)\n\n")
	}
	b.WriteString("## Commits\n")
	for _, c := range pr.Commits {
		b.WriteString("- " + c + "\n")
	}
	b.WriteString("\n## Your Task\n")
	b.WriteString("Perform a thorough code review of this pull request:\n")
	b.WriteString("1. Read and understand the PR description and commit messages above carefully\n")
	b.WriteString("2. Examine all changed files in this branch\n")
	b.WriteString("3. Check for: bugs, security issues, performance problems, code quality, readability\n")
	b.WriteString("4. Provide specific, actionable feedback with file paths and line numbers\n")
	b.WriteString("5. Summarize your overall assessment\n")
	return b.String()
}

func launchClaude(db *PRReviewDB, workDir, prompt string) error {
	claudePath := findClaude(db)
	if claudePath == "" {
		return fmt.Errorf("claude not found — set its path in Settings (tried PATH and common locations)")
	}

	// Write prompt to temp file to avoid shell quoting issues
	promptFile := filepath.Join(os.TempDir(), fmt.Sprintf("moraikeb-pr-%d.md", time.Now().UnixNano()))
	if err := os.WriteFile(promptFile, []byte(prompt), 0644); err != nil {
		return err
	}

	// Write a launcher script using the absolute path to claude
	script := fmt.Sprintf(`#!/bin/bash
cd %q
PROMPT=$(cat %q)
rm -f %q
rm -f "$0"
exec %q "$PROMPT"
`, workDir, promptFile, promptFile, claudePath)

	scriptFile := filepath.Join(os.TempDir(), fmt.Sprintf("moraikeb-launch-%d.sh", time.Now().UnixNano()))
	if err := os.WriteFile(scriptFile, []byte(script), 0755); err != nil {
		return err
	}

	// Try terminal emulators in order of popularity
	// Note: each terminal has different -e semantics; some take a single
	// string, others take separate args. We use separate args where possible.
	terminals := []struct {
		name string
		args []string
	}{
		{"ghostty", []string{"-e", "bash", scriptFile}},
		{"gnome-terminal", []string{"--", "bash", scriptFile}},
		{"konsole", []string{"-e", "bash", scriptFile}},
		{"xfce4-terminal", []string{"-e", "bash " + scriptFile}},
		{"x-terminal-emulator", []string{"-e", "bash", scriptFile}},
		{"xterm", []string{"-e", "bash", scriptFile}},
	}

	for _, t := range terminals {
		path, err := exec.LookPath(t.name)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, t.args...)
		if err := cmd.Start(); err != nil {
			log.Printf("failed to start %s: %v", t.name, err)
			continue
		}
		log.Printf("launched claude review in %s via %s", workDir, t.name)
		return nil
	}

	// Cleanup on failure
	os.Remove(scriptFile)
	os.Remove(promptFile)
	return fmt.Errorf("no terminal emulator found (tried gnome-terminal, konsole, xfce4-terminal, x-terminal-emulator, xterm)")
}

// findClaude returns the absolute path to the claude binary.
// Checks: config setting → exec.LookPath → common install locations.
func findClaude(db *PRReviewDB) string {
	// 1. Check user-configured path
	if db != nil {
		if p := db.GetConfig("claude_path"); p != "" {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}

	// 2. Try PATH
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}

	// 3. Try common locations
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "claude"),
		filepath.Join(home, ".npm-global", "bin", "claude"),
		"/usr/local/bin/claude",
		"/snap/bin/claude",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ""
}

// --- HTTP handlers ---

func (s *Server) handlePRLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "bad json", http.StatusBadRequest)
		return
	}

	parsed, err := parsePRURL(req.URL)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check stored repo
	stored, err := s.prDB.GetRepo(parsed.Owner, parsed.Repo)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Fetch PR info from GitHub
	prInfo, err := fetchPRInfoRouted(s.prDB, parsed.Owner, parsed.Repo, parsed.Number)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"pr":          prInfo,
		"stored_repo": stored,
	})
}

func (s *Server) handlePRStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URL      string `json:"url"`
		RepoPath string `json:"repo_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "bad json", http.StatusBadRequest)
		return
	}

	parsed, err := parsePRURL(req.URL)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Validate repo path
	repoPath := filepath.Clean(req.RepoPath)
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		jsonError(w, "not a git repository: "+repoPath, http.StatusBadRequest)
		return
	}

	// Save repo mapping for next time
	if err := s.prDB.SaveRepo(parsed.Owner, parsed.Repo, repoPath); err != nil {
		log.Printf("save repo mapping: %v", err)
	}

	// Fetch PR info
	prInfo, err := fetchPRInfoRouted(s.prDB, parsed.Owner, parsed.Repo, parsed.Number)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Start the review
	worktreePath, err := startReview(s.prDB, repoPath, prInfo)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "ok",
		"worktree":  worktreePath,
		"pr_title":  prInfo.Title,
		"pr_branch": prInfo.Branch,
	})
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath, _ = os.UserHomeDir()
	}
	dirPath = filepath.Clean(dirPath)

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		jsonError(w, "cannot read directory: "+err.Error(), http.StatusBadRequest)
		return
	}

	type DirEntry struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsGit bool   `json:"is_git"`
	}

	var dirs []DirEntry
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fullPath := filepath.Join(dirPath, e.Name())
		_, gitErr := os.Stat(filepath.Join(fullPath, ".git"))
		dirs = append(dirs, DirEntry{
			Name:  e.Name(),
			Path:  fullPath,
			IsGit: gitErr == nil,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"path":    dirPath,
		"parent":  filepath.Dir(dirPath),
		"entries": dirs,
	})
}

func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := s.prDB.ListRepos()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"repos": repos,
	})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.prDB.GetAllConfig()
	// Mask the token for security
	if t, ok := cfg["github_token"]; ok && len(t) > 8 {
		cfg["github_token"] = t[:4] + "..." + t[len(t)-4:]
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

func (s *Server) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req map[string]string
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "bad json", http.StatusBadRequest)
		return
	}

	// Only allow known config keys
	allowed := map[string]bool{"github_method": true, "github_token": true, "claude_path": true}
	for k, v := range req {
		if !allowed[k] {
			continue
		}
		if err := s.prDB.SetConfig(k, v); err != nil {
			jsonError(w, "save config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("config: %s = %s", k, func() string {
			if k == "github_token" {
				return "(redacted)"
			}
			return v
		}())
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
