// Package hn is a minimal client for the Hacker News Firebase API.
//
// Only the handful of endpoints we need are covered:
//   - topstories.json : list of current front-page story IDs
//   - item/{id}.json  : metadata for one item (story, ask, poll, etc.)
//
// See https://github.com/HackerNews/API for the full reference.
package hn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

const baseURL = "https://hacker-news.firebaseio.com/v0"

// Item is a single Hacker News item. Fields are optional — Firebase omits
// them when they don't apply (e.g. "url" is missing on Ask HN posts).
type Item struct {
	ID          int    `json:"id"`
	Type        string `json:"type"` // "story", "comment", "job", "ask", "poll", "pollopt"
	By          string `json:"by"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Text        string `json:"text"` // HTML — present on Ask HN etc.
	Score       int    `json:"score"`
	Time        int64  `json:"time"` // Unix seconds
	Descendants int    `json:"descendants"`
	Dead        bool   `json:"dead"`
	Deleted     bool   `json:"deleted"`
}

// PermalinkURL returns the canonical news.ycombinator.com URL for the item.
func (i Item) PermalinkURL() string {
	return "https://news.ycombinator.com/item?id=" + strconv.Itoa(i.ID)
}

// Client talks to the public HN API. The zero value is not usable; use New.
type Client struct {
	http *http.Client
	ua   string
}

// New returns a Client that uses the given *http.Client (must not be nil) and
// identifies itself with ua on each request.
func New(h *http.Client, ua string) *Client {
	return &Client{http: h, ua: ua}
}

// TopStories returns the current top-story IDs, in rank order, truncated to
// the first n. The upstream list is up to 500 long.
func (c *Client) TopStories(ctx context.Context, n int) ([]int, error) {
	var ids []int
	if err := c.get(ctx, baseURL+"/topstories.json", &ids); err != nil {
		return nil, fmt.Errorf("topstories: %w", err)
	}
	if n > 0 && n < len(ids) {
		ids = ids[:n]
	}
	return ids, nil
}

// GetItem fetches one item by ID. If the item is deleted or dead, the
// returned Item may have mostly-zero fields; callers should check.
func (c *Client) GetItem(ctx context.Context, id int) (Item, error) {
	var it Item
	u := baseURL + "/item/" + url.PathEscape(strconv.Itoa(id)) + ".json"
	if err := c.get(ctx, u, &it); err != nil {
		return Item{}, fmt.Errorf("item %d: %w", id, err)
	}
	return it, nil
}

func (c *Client) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if c.ua != "" {
		req.Header.Set("User-Agent", c.ua)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
