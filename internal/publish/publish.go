// Package publish writes the digest into a local clone of the target
// GitHub Pages repo, regenerates the index, and commits + pushes.
//
// It shells out to the system `git` binary so it can reuse the user's
// existing SSH credentials. The repo is cloned on first use if missing.
package publish

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// File is one artifact to write into the target repo's Subdir.
type File struct {
	Name    string // base name, e.g. "2026-04-13.html"
	Content []byte
}

// Options controls Publish. All fields are required unless noted.
type Options struct {
	// TargetRepoDir is the local working copy of the GitHub Pages repo.
	// Created (via git clone) if it doesn't exist yet.
	TargetRepoDir string

	// RemoteURL is used only if TargetRepoDir doesn't exist.
	RemoteURL string

	// Branch to pull / commit / push on.
	Branch string

	// Subdir under TargetRepoDir where digests live, e.g. "hn".
	Subdir string

	// Files to write into Subdir this run.
	Files []File

	// CommitMessage to use.
	CommitMessage string

	// DryRun: write files locally but don't git add/commit/push.
	DryRun bool

	// NoPush: commit locally but skip the push step.
	NoPush bool
}

// Publish runs the full flow: ensure-clone → pull → write files →
// regenerate index → commit → push (each step respecting DryRun/NoPush).
// Returns (didCommit, err). didCommit is false when there were no
// changes to commit.
func Publish(opts Options) (bool, error) {
	if err := ensureRepo(opts.TargetRepoDir, opts.RemoteURL, opts.Branch); err != nil {
		return false, fmt.Errorf("ensure repo: %w", err)
	}
	if !opts.DryRun {
		if err := git(opts.TargetRepoDir, "pull", "--ff-only", "origin", opts.Branch); err != nil {
			return false, fmt.Errorf("git pull: %w", err)
		}
	}

	subdirAbs := filepath.Join(opts.TargetRepoDir, opts.Subdir)
	if err := os.MkdirAll(subdirAbs, 0o755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", subdirAbs, err)
	}

	for _, f := range opts.Files {
		path := filepath.Join(subdirAbs, f.Name)
		if err := os.WriteFile(path, f.Content, 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", path, err)
		}
	}

	if err := pruneOld(subdirAbs, 7); err != nil {
		return false, fmt.Errorf("prune old digests: %w", err)
	}

	if err := writeIndex(subdirAbs); err != nil {
		return false, fmt.Errorf("write index: %w", err)
	}

	if opts.DryRun {
		return false, nil
	}

	// Stage just our subdir so we never accidentally pick up unrelated changes.
	if err := git(opts.TargetRepoDir, "add", "--", opts.Subdir); err != nil {
		return false, fmt.Errorf("git add: %w", err)
	}

	clean, err := isCleanStaged(opts.TargetRepoDir)
	if err != nil {
		return false, err
	}
	if clean {
		return false, nil
	}

	if err := git(opts.TargetRepoDir, "commit", "-m", opts.CommitMessage); err != nil {
		return false, fmt.Errorf("git commit: %w", err)
	}
	if opts.NoPush {
		return true, nil
	}
	if err := git(opts.TargetRepoDir, "push", "origin", opts.Branch); err != nil {
		return true, fmt.Errorf("git push: %w", err)
	}
	return true, nil
}

func ensureRepo(dir, remote, branch string) error {
	if dir == "" {
		return fmt.Errorf("target repo dir is empty")
	}
	gitDir := filepath.Join(dir, ".git")
	if st, err := os.Stat(gitDir); err == nil && st.IsDir() {
		return nil
	}
	if remote == "" {
		return fmt.Errorf("%s does not exist and no remote URL was provided", dir)
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	return git(parent, "clone", "--branch", branch, remote, filepath.Base(dir))
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%s: %s", err, msg)
		}
		return err
	}
	return nil
}

func isCleanStaged(dir string) (bool, error) {
	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git diff --cached: %w", err)
}

// digestFilePattern matches our dated digest files. The capture groups expose
// the date and extension so we can group multiple formats (html, epub) per day.
var digestFilePattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.(html|epub)$`)

// pruneOld removes digest files older than keepDays from subdirAbs.
func pruneOld(subdirAbs string, keepDays int) error {
	cutoff := time.Now().UTC().AddDate(0, 0, -keepDays).Format("2006-01-02")

	ents, err := os.ReadDir(subdirAbs)
	if err != nil {
		return err
	}
	for _, e := range ents {
		m := digestFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if m[1] < cutoff {
			if err := os.Remove(filepath.Join(subdirAbs, e.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func writeIndex(subdirAbs string) error {
	ents, err := os.ReadDir(subdirAbs)
	if err != nil {
		return err
	}
	// Map date -> ext -> indexFile.
	byDate := map[string]map[string]indexFile{}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		m := digestFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		date, ext := m[1], m[2]
		info, err := e.Info()
		if err != nil && !isNotExist(err) {
			return err
		}
		if byDate[date] == nil {
			byDate[date] = map[string]indexFile{}
		}
		byDate[date][ext] = indexFile{
			Name: e.Name(),
			Size: humanSize(info.Size()),
		}
	}

	dates := make([]string, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))

	entries := make([]indexEntry, 0, len(dates))
	for _, d := range dates {
		entries = append(entries, indexEntry{
			Date: d,
			HTML: byDate[d]["html"],
			EPUB: byDate[d]["epub"],
		})
	}

	data := indexData{
		Entries: entries,
		Updated: time.Now().UTC().Format("2006-01-02 15:04 UTC"),
	}
	var buf bytes.Buffer
	if err := indexTmpl.Execute(&buf, data); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(subdirAbs, "index.html"), buf.Bytes(), 0o644); err != nil {
		return err
	}

	// Write PWA manifest and service worker.
	manifest := `{
  "name": "Hacker News Digests",
  "short_name": "HN Digests",
  "start_url": "./index.html",
  "display": "standalone",
  "background_color": "#fafafa",
  "theme_color": "#0645ad",
  "icons": [
    {
      "src": "https://news.ycombinator.com/favicon.ico",
      "sizes": "16x16",
      "type": "image/x-icon"
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(subdirAbs, "manifest.json"), []byte(manifest), 0o644); err != nil {
		return err
	}

	sw := `const CACHE_NAME = 'hn-digests-v1';
const ASSETS = [
  './',
  './index.html',
  './manifest.json'
];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => cache.addAll(ASSETS))
  );
});

self.addEventListener('fetch', (event) => {
  event.respondWith(
    fetch(event.request).catch(() => caches.match(event.request))
  );
});`
	return os.WriteFile(filepath.Join(subdirAbs, "sw.js"), []byte(sw), 0o644)
}

func isNotExist(err error) bool {
	return err != nil && (os.IsNotExist(err) || err == fs.ErrNotExist)
}

func humanSize(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
	)
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1f MiB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1f KiB", float64(n)/kb)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
