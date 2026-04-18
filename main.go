// hn-parser: fetch the HN top stories, scrape each linked page to plain text,
// and publish a dated digest to the hn/ folder of denislee.github.io.
//
// Usage:
//
//	hn-parser [flags]
//
// See -help for flags.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/denislee/hn-parser/internal/digest"
	"github.com/denislee/hn-parser/internal/hn"
	"github.com/denislee/hn-parser/internal/publish"
	"github.com/denislee/hn-parser/internal/scrape"
)

const (
	defaultRemote    = "git@github.com:denislee/denislee.github.io.git"
	defaultBranch    = "master"
	defaultSubdir    = "hn"
	defaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

func main() {
	var (
		count       = flag.Int("n", 30, "number of HN top stories to include")
		concurrency = flag.Int("c", 6, "parallel scrape workers")
		timeout     = flag.Duration("timeout", 30*time.Second, "per-URL scrape timeout")
		target      = flag.String("target", defaultTargetDir(), "local working clone of the target repo")
		remote      = flag.String("remote", defaultRemote, "git remote URL for the target repo (used only if -target doesn't exist)")
		branch      = flag.String("branch", defaultBranch, "branch to commit/push on the target repo")
		subdir      = flag.String("subdir", defaultSubdir, "subdirectory inside the target repo to publish into")
		dryRun      = flag.Bool("dry-run", false, "write files locally but don't commit or push")
		noPush      = flag.Bool("no-push", false, "commit locally but don't push")
	)
	flag.Parse()

	if *count < 1 {
		fatal("invalid -n: must be >= 1")
	}
	if *concurrency < 1 {
		fatal("invalid -c: must be >= 1")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runTime := time.Now().UTC()
	httpClient := &http.Client{Timeout: 30 * time.Second}
	hnClient := hn.New(httpClient, defaultUserAgent)
	scraper := scrape.New(defaultUserAgent, *timeout)

	log.Printf("fetching top %d story IDs", *count)
	ids, err := hnClient.TopStories(ctx, *count)
	if err != nil {
		fatal("top stories: %v", err)
	}

	log.Printf("fetching %d item metadata records", len(ids))
	items, err := fetchItems(ctx, hnClient, ids, *concurrency)
	if err != nil {
		fatal("fetch items: %v", err)
	}

	log.Printf("scraping %d URLs with %d workers", len(items), *concurrency)
	entries := scrapeAll(ctx, scraper, items, *concurrency, *timeout)

	htmlBody, err := digest.Render(runTime, entries)
	if err != nil {
		fatal("render html digest: %v", err)
	}
	epubBody, err := digest.RenderEPUB(runTime, entries)
	if err != nil {
		fatal("render epub digest: %v", err)
	}
	mdBody, err := digest.RenderMD(runTime, entries)
	if err != nil {
		fatal("render md digest: %v", err)
	}
	simplifiedMDBody, err := digest.RenderSimplifiedMD(runTime, entries)
	if err != nil {
		fatal("render simplified md digest: %v", err)
	}

	date := runTime.Format("2006-01-02")
	files := []publish.File{
		{Name: date + ".html", Content: []byte(htmlBody)},
		{Name: date + ".epub", Content: epubBody},
		{Name: date + ".md", Content: mdBody},
		{Name: date + "-simplified.md", Content: simplifiedMDBody},
	}

	for _, f := range files {
		log.Printf("  %s (%d bytes)", f.Name, len(f.Content))
	}
	log.Printf("publishing %d files to %s/%s", len(files), *target, *subdir)

	didCommit, err := publish.Publish(publish.Options{
		TargetRepoDir: *target,
		RemoteURL:     *remote,
		Branch:        *branch,
		Subdir:        *subdir,
		Files:         files,
		CommitMessage: fmt.Sprintf("hn: digest for %s (%d stories)", date, len(entries)),
		DryRun:        *dryRun,
		NoPush:        *noPush,
	})
	if err != nil {
		fatal("publish: %v", err)
	}

	switch {
	case *dryRun:
		log.Printf("dry-run complete; files written under %s", filepath.Join(*target, *subdir))
	case !didCommit:
		log.Printf("no changes to commit")
	case *noPush:
		log.Printf("committed locally (push skipped)")
	default:
		log.Printf("committed and pushed to %s/%s", *remote, *branch)
	}
}

// fetchItems returns items in the same order as ids. Items that return an
// error, are deleted, dead, or aren't stories are dropped.
func fetchItems(ctx context.Context, c *hn.Client, ids []int, workers int) ([]hn.Item, error) {
	type result struct {
		rank int
		item hn.Item
		err  error
	}
	jobs := make(chan struct {
		rank int
		id   int
	}, len(ids))
	results := make(chan result, len(ids))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				it, err := c.GetItem(ctx, j.id)
				results <- result{rank: j.rank, item: it, err: err}
			}
		}()
	}
	for i, id := range ids {
		jobs <- struct {
			rank int
			id   int
		}{i, id}
	}
	close(jobs)
	go func() { wg.Wait(); close(results) }()

	keep := make([]result, 0, len(ids))
	for r := range results {
		if r.err != nil {
			log.Printf("  item %d: %v (skipping)", r.item.ID, r.err)
			continue
		}
		if r.item.Dead || r.item.Deleted {
			continue
		}
		// Include "story" and "job"; skip comments/polls.
		if r.item.Type != "story" && r.item.Type != "job" {
			continue
		}
		keep = append(keep, r)
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].rank < keep[j].rank })
	out := make([]hn.Item, len(keep))
	for i, r := range keep {
		out[i] = r.item
	}
	return out, nil
}

func scrapeAll(ctx context.Context, s *scrape.Scraper, items []hn.Item, workers int, timeout time.Duration) []digest.Entry {
	type job struct {
		idx  int
		item hn.Item
	}
	jobs := make(chan job, len(items))
	entries := make([]digest.Entry, len(items))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				e := digest.Entry{Item: j.item}
				if j.item.URL == "" {
					// Ask/Show HN — no external URL, handled by digest from item.Text.
					entries[j.idx] = e
					continue
				}
				res, err := s.Fetch(ctx, j.item.URL, timeout)
				if err != nil {
					log.Printf("  [%d] %s: %v", j.idx+1, j.item.URL, err)
					e.Err = err
				} else {
					e.Scrape = res
				}
				entries[j.idx] = e
			}
		}()
	}
	for i, it := range items {
		jobs <- job{idx: i, item: it}
	}
	close(jobs)
	wg.Wait()
	return entries
}

func defaultTargetDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "src", "denislee.github.io")
	}
	return "./denislee.github.io"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "hn-parser: "+format+"\n", args...)
	os.Exit(1)
}
