// Package digest formats a set of scraped HN stories into one HTML document.
package digest

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/denislee/hn-parser/internal/hn"
	"github.com/denislee/hn-parser/internal/scrape"
)

// Entry is one story in the digest.
type Entry struct {
	Item   hn.Item
	Scrape *scrape.Result // nil if scrape wasn't attempted or failed outright
	Err    error          // scrape error, if any
}

// Render produces the HTML digest body.
func Render(runTime time.Time, entries []Entry) (string, error) {
	view := newView(runTime, entries)
	var buf bytes.Buffer
	if err := digestTmpl.Execute(&buf, view); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// --- view model ------------------------------------------------------------

type sourceRow struct {
	Label string
	Value template.HTML // may contain links, so trusted
}

type viewEntry struct {
	Rank       int
	Anchor     string
	Title      string
	HNLink     string
	URL        string
	By         string
	Score      int
	Comments   int
	Posted     string
	SiteName   string
	Byline     string
	Excerpt    string
	Source     []sourceRow   // rows of the info card
	Body       template.HTML // trusted: from go-readability or HN's own item.Text
	Note       string        // error / skipped / "no content" label
}

type view struct {
	Title    string
	RunTime  string
	Count    int
	Entries  []viewEntry
}

func newView(runTime time.Time, entries []Entry) view {
	v := view{
		Title:   fmt.Sprintf("Hacker News Top %d — %s", len(entries), runTime.UTC().Format("2006-01-02")),
		RunTime: runTime.UTC().Format("2006-01-02 15:04 UTC"),
		Count:   len(entries),
		Entries: make([]viewEntry, 0, len(entries)),
	}
	for i, e := range entries {
		v.Entries = append(v.Entries, buildEntry(i+1, e))
	}
	return v
}

func buildEntry(rank int, e Entry) viewEntry {
	it := e.Item
	ve := viewEntry{
		Rank:     rank,
		Anchor:   fmt.Sprintf("story-%d", rank),
		Title:    orFallback(it.Title, "(no title)"),
		HNLink:   it.PermalinkURL(),
		URL:      it.URL,
		By:       it.By,
		Score:    it.Score,
		Comments: it.Descendants,
	}
	if it.Time > 0 {
		ve.Posted = time.Unix(it.Time, 0).UTC().Format("2006-01-02 15:04 UTC")
	}
	if e.Scrape != nil {
		ve.SiteName = e.Scrape.SiteName
		ve.Byline = e.Scrape.Byline
		ve.Excerpt = e.Scrape.Excerpt
	}

	// Build source card rows (same logic as the EPUB buildSourceBox).
	ve.Source = buildHTMLSourceRows(e)

	switch {
	case e.Err != nil:
		ve.Note = "scrape failed: " + e.Err.Error()
	case e.Scrape != nil && e.Scrape.Skipped != "":
		ve.Note = e.Scrape.Skipped
	case e.Scrape != nil && strings.TrimSpace(e.Scrape.HTML) != "":
		// go-readability already strips <script> and dangerous attributes.
		ve.Body = template.HTML(e.Scrape.HTML) // #nosec G203 -- sanitized upstream
	case strings.TrimSpace(it.Text) != "":
		// Ask/Show HN body is HN-controlled HTML (<p>, <a>, <i>, <pre>).
		ve.Body = template.HTML(it.Text) // #nosec G203 -- from HN API
	default:
		ve.Note = "no extractable content"
	}
	return ve
}

func buildHTMLSourceRows(e Entry) []sourceRow {
	it := e.Item
	var rows []sourceRow
	add := func(label string, valueHTML template.HTML) {
		if string(valueHTML) == "" {
			return
		}
		rows = append(rows, sourceRow{Label: label, Value: valueHTML})
	}

	// Source URL.
	switch {
	case it.URL != "":
		add("Source", template.HTML(fmt.Sprintf(
			`<a href="%s">%s</a>`,
			template.HTMLEscapeString(it.URL),
			template.HTMLEscapeString(it.URL))))
	default:
		add("Source", template.HTML(fmt.Sprintf(
			`<a href="%s">%s</a> (Hacker News text post)`,
			template.HTMLEscapeString(it.PermalinkURL()),
			template.HTMLEscapeString(it.PermalinkURL()))))
	}

	// Redirect detection.
	if e.Scrape != nil && e.Scrape.FinalURL != "" && it.URL != "" && e.Scrape.FinalURL != it.URL {
		add("Redirected to", template.HTML(fmt.Sprintf(
			`<a href="%s">%s</a>`,
			template.HTMLEscapeString(e.Scrape.FinalURL),
			template.HTMLEscapeString(e.Scrape.FinalURL))))
	}

	// Site.
	site := ""
	if e.Scrape != nil {
		site = e.Scrape.SiteName
	}
	if site == "" {
		if it.URL != "" {
			site = shortHost(it.URL)
		} else {
			site = "news.ycombinator.com"
		}
	}
	add("Site", template.HTML(template.HTMLEscapeString(site)))

	// Author / Submitter.
	author := ""
	if e.Scrape != nil {
		author = e.Scrape.Byline
	}
	switch {
	case author != "":
		add("Author", template.HTML(template.HTMLEscapeString(author)))
	case it.By != "":
		add("Submitter", template.HTML(
			template.HTMLEscapeString(it.By)+` <span class="hn-tag">(Hacker News)</span>`))
	}

	// Published / Submitted.
	switch {
	case e.Scrape != nil && !e.Scrape.PublishedTime.IsZero():
		add("Published", template.HTML(
			template.HTMLEscapeString(e.Scrape.PublishedTime.UTC().Format("2006-01-02"))))
	case it.Time > 0:
		add("Submitted", template.HTML(
			template.HTMLEscapeString(time.Unix(it.Time, 0).UTC().Format("2006-01-02 15:04 UTC"))+
				` <span class="hn-tag">(Hacker News)</span>`))
	}

	// HN activity.
	add("HN activity", template.HTML(fmt.Sprintf(
		`%d points · <a href="%s">%d comments</a>`,
		it.Score,
		template.HTMLEscapeString(it.PermalinkURL()),
		it.Descendants)))

	// Length.
	if e.Scrape != nil && e.Scrape.WordCount > 0 {
		add("Length", template.HTML(fmt.Sprintf("%s (~%d min read)",
			formatCount(e.Scrape.WordCount, "word"),
			readingTimeMinutes(e.Scrape.WordCount))))
	}

	// Language.
	if e.Scrape != nil && e.Scrape.Language != "" {
		add("Language", template.HTML(template.HTMLEscapeString(e.Scrape.Language)))
	}

	return rows
}

func orFallback(s, fb string) string {
	if strings.TrimSpace(s) == "" {
		return fb
	}
	return s
}

// --- template --------------------------------------------------------------

var digestTmpl = template.Must(template.New("digest").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{ .Title }}</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
 :root{--fg:#222;--muted:#666;--bg:#fafafa;--card:#fff;--border:#e5e5e5;--link:#0645ad}
 *{box-sizing:border-box}
 body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem;color:var(--fg);background:var(--bg);line-height:1.55}
 header{margin-bottom:2rem}
 h1{font-size:1.5rem;margin:0 0 0.25rem}
 header p{color:var(--muted);margin:0}
 header a{color:var(--muted)}
 nav.toc{background:var(--card);border:1px solid var(--border);border-radius:6px;padding:0.8rem 1rem;margin-bottom:2rem;font-size:0.92em}
 nav.toc summary{cursor:pointer;font-weight:600;color:var(--muted)}
 nav.toc ol{margin:0.6rem 0 0;padding-left:1.4rem}
 nav.toc li{margin:0.15rem 0}
 nav.toc a{color:var(--link);text-decoration:none}
 nav.toc a:hover{text-decoration:underline}
 article{background:var(--card);border:1px solid var(--border);border-radius:6px;padding:1.1rem 1.3rem;margin-bottom:1.5rem}
 article h2{font-size:1.15rem;margin:0 0 0.35rem;line-height:1.3}
 article h2 .rank{color:var(--muted);font-weight:500;margin-right:0.35rem}
 article h2 a{color:var(--fg);text-decoration:none}
 article h2 a:hover{text-decoration:underline}
 .source{background:var(--bg);border-left:3px solid var(--border);padding:0.5rem 0.9rem;margin:0.6rem 0 1rem;font-size:0.88em;color:var(--muted)}
 .source p{margin:0.15rem 0;display:flex;gap:0.4rem;flex-wrap:wrap}
 .source .label{color:#888;min-width:6.5em;flex-shrink:0}
 .source .val{word-break:break-all}
 .source a{color:var(--link);text-decoration:none}
 .source a:hover{text-decoration:underline}
 .hn-tag{color:#aaa;font-size:0.9em}
 .excerpt{border-left:3px solid var(--border);color:var(--muted);padding:0.1rem 0.9rem;margin:0 0 1rem;font-style:italic;font-size:0.93em}
 .body{font-size:0.97em}
 .body img,.body video{max-width:100%;height:auto;border-radius:4px}
 .body pre{overflow-x:auto;background:#f3f3f3;padding:0.6rem 0.8rem;border-radius:4px;font-size:0.88em}
 .body code{background:#f3f3f3;padding:0.05em 0.35em;border-radius:3px;font-size:0.9em}
 .body pre code{background:transparent;padding:0}
 .body blockquote{border-left:3px solid var(--border);color:var(--muted);margin:0.8rem 0;padding:0.1rem 0.9rem}
 .body h1,.body h2,.body h3,.body h4{font-size:1.02em;margin:1em 0 0.3em}
 .body a{color:var(--link)}
 .note{color:var(--muted);font-style:italic}
 .backtop{margin-top:0.8rem;font-size:0.85em}
 .backtop a{color:var(--muted);text-decoration:none}
 footer{margin:3rem 0 1rem;color:var(--muted);font-size:0.85em;text-align:center}
</style>
</head>
<body>
<header>
<h1>{{ .Title }}</h1>
<p>Generated {{ .RunTime }} · <a href="./">← all digests</a></p>
</header>

<nav class="toc" id="top">
<details open>
<summary>Contents ({{ .Count }} stories)</summary>
<ol>
{{- range .Entries }}
  <li><a href="#{{ .Anchor }}">{{ .Title }}</a></li>
{{- end }}
</ol>
</details>
</nav>

{{ range .Entries }}
<article id="{{ .Anchor }}">
  <h2><span class="rank">{{ .Rank }}.</span>{{ if .URL }}<a href="{{ .URL }}" target="_blank" rel="noopener noreferrer">{{ .Title }}</a>{{ else }}<a href="{{ .HNLink }}" target="_blank" rel="noopener noreferrer">{{ .Title }}</a>{{ end }}</h2>
  {{- if .Source }}
  <div class="source">
  {{- range .Source }}
    <p><span class="label">{{ .Label }}</span><span class="val">{{ .Value }}</span></p>
  {{- end }}
  </div>
  {{- end }}
  {{- if .Excerpt }}
  <div class="excerpt">{{ .Excerpt }}</div>
  {{- end }}
  {{- if .Note }}
  <p class="note">[{{ .Note }}]</p>
  {{- else }}
  <div class="body">{{ .Body }}</div>
  {{- end }}
  <p class="backtop"><a href="#top">↑ top</a></p>
</article>
{{ end }}

<footer>Generated by <a href="https://github.com/denislee/hn-parser">hn-parser</a>.</footer>
</body>
</html>
`))
