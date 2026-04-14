package digest

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-shiori/go-epub"
)

const epubCSS = `
body { font-family: Georgia, serif; line-height: 1.55; }
h1 { font-size: 1.3em; margin: 0 0 0.4em; }
.meta { font-size: 0.85em; color: #555; margin: 0.5em 0 0.2em; line-height: 1.4; }
.meta a { color: #0645ad; text-decoration: none; }
.source { background: #f4f4f4; border-left: 3px solid #bbb; padding: 0.6em 0.9em; margin: 0.8em 0 1.2em; font-size: 0.88em; }
.source p { margin: 0.2em 0; }
.source .label { color: #666; display: inline-block; min-width: 5.5em; }
.source .url { word-break: break-all; }
.excerpt { border-left: 3px solid #ccc; color: #555; padding: 0.1em 0.9em; margin: 1em 0; font-style: italic; }
blockquote { border-left: 3px solid #ccc; color: #555; padding: 0.1em 0.9em; margin: 0.8em 0; }
pre { background: #f3f3f3; padding: 0.6em 0.8em; overflow-x: auto; font-size: 0.9em; }
code { background: #f3f3f3; padding: 0.05em 0.3em; font-size: 0.9em; }
pre code { background: transparent; padding: 0; }
img { max-width: 100%; height: auto; }
.note { color: #777; font-style: italic; }
`

// RenderEPUB produces an EPUB file (bytes) with one chapter per story.
func RenderEPUB(runTime time.Time, entries []Entry) ([]byte, error) {
	title := fmt.Sprintf("Hacker News Top %d — %s", len(entries), runTime.UTC().Format("2006-01-02"))

	book, err := epub.NewEpub(title)
	if err != nil {
		return nil, fmt.Errorf("new epub: %w", err)
	}
	book.SetAuthor("hn-parser")
	book.SetLang("en")
	book.SetDescription(fmt.Sprintf("Readable digest of the Hacker News top %d stories, generated %s.",
		len(entries), runTime.UTC().Format("2006-01-02 15:04 UTC")))

	cssDataURL := "data:text/css;base64," + base64.StdEncoding.EncodeToString([]byte(epubCSS))
	cssPath, err := book.AddCSS(cssDataURL, "style.css")
	if err != nil {
		return nil, fmt.Errorf("add css: %w", err)
	}

	for i, e := range entries {
		body, secTitle := buildChapter(i+1, e)
		if _, err := book.AddSection(body, secTitle, fmt.Sprintf("story-%04d.xhtml", i+1), cssPath); err != nil {
			return nil, fmt.Errorf("add section %d: %w", i+1, err)
		}
	}

	var buf bytes.Buffer
	if _, err := book.WriteTo(&buf); err != nil {
		return nil, fmt.Errorf("write epub: %w", err)
	}
	return buf.Bytes(), nil
}

func buildChapter(rank int, e Entry) (body, title string) {
	it := e.Item
	title = orFallback(it.Title, "(no title)")

	var sb strings.Builder

	fmt.Fprintf(&sb, `<h1>%d. %s</h1>`, rank, escape(title))

	// Source info box (URL, site, article metadata, HN stats).
	sb.WriteString(buildSourceBox(e))

	// Excerpt if we have one.
	if e.Scrape != nil && e.Scrape.Excerpt != "" && strings.TrimSpace(e.Scrape.HTML) != "" {
		fmt.Fprintf(&sb, `<div class="excerpt">%s</div>`, escape(e.Scrape.Excerpt))
	}

	// Body.
	switch {
	case e.Err != nil:
		fmt.Fprintf(&sb, `<p class="note">[scrape failed: %s]</p>`, escape(e.Err.Error()))
	case e.Scrape != nil && e.Scrape.Skipped != "":
		fmt.Fprintf(&sb, `<p class="note">[%s]</p>`, escape(e.Scrape.Skipped))
	case e.Scrape != nil && strings.TrimSpace(e.Scrape.HTML) != "":
		sb.WriteString(xhtmlify(e.Scrape.HTML))
	case strings.TrimSpace(it.Text) != "":
		sb.WriteString(xhtmlify(it.Text))
	default:
		sb.WriteString(`<p class="note">[no extractable content]</p>`)
	}
	return sb.String(), title
}

// buildSourceBox renders a styled info box with the story's source URL and
// any article metadata we managed to extract. Tries to keep rows consistent
// across stories by using sensible fallbacks (hostname for site, HN
// submission time for published, etc.).
func buildSourceBox(e Entry) string {
	it := e.Item

	var rows []string
	add := func(label, valueHTML string) {
		if valueHTML == "" {
			return
		}
		rows = append(rows, fmt.Sprintf(`<p><span class="label">%s</span>%s</p>`, escape(label), valueHTML))
	}

	// Source URL (full, clickable). Falls back to the HN item itself for
	// Ask/Show HN posts that have no external URL.
	switch {
	case it.URL != "":
		add("Source:", fmt.Sprintf(`<a class="url" href="%s">%s</a>`, escape(it.URL), escape(it.URL)))
	default:
		add("Source:", fmt.Sprintf(`<a class="url" href="%s">%s</a> (Hacker News text post)`, escape(it.PermalinkURL()), escape(it.PermalinkURL())))
	}

	// If the scraper followed redirects to a different URL, surface that too.
	if e.Scrape != nil && e.Scrape.FinalURL != "" && it.URL != "" && e.Scrape.FinalURL != it.URL {
		add("Redirected to:", fmt.Sprintf(`<a class="url" href="%s">%s</a>`, escape(e.Scrape.FinalURL), escape(e.Scrape.FinalURL)))
	}

	// Site: readability's extracted SiteName → hostname fallback → "news.ycombinator.com".
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
	add("Site:", escape(site))

	// Author: readability byline → HN submitter (labelled as such).
	author := ""
	if e.Scrape != nil {
		author = e.Scrape.Byline
	}
	switch {
	case author != "":
		add("Author:", escape(author))
	case it.By != "":
		add("Submitter:", escape(it.By)+` <span class="label" style="min-width:0">(Hacker News)</span>`)
	}

	// Published: readability's article date → HN submission time.
	switch {
	case e.Scrape != nil && !e.Scrape.PublishedTime.IsZero():
		add("Published:", escape(e.Scrape.PublishedTime.UTC().Format("2006-01-02")))
	case it.Time > 0:
		add("Submitted:", escape(time.Unix(it.Time, 0).UTC().Format("2006-01-02 15:04 UTC"))+` <span class="label" style="min-width:0">(Hacker News)</span>`)
	}

	// HN stats (always available).
	add("HN activity:", fmt.Sprintf(`%d points · <a href="%s">%d comments</a>`,
		it.Score, escape(it.PermalinkURL()), it.Descendants))

	// Length / reading time (from scraped content).
	if e.Scrape != nil && e.Scrape.WordCount > 0 {
		add("Length:", fmt.Sprintf("%s (~%d min read)",
			formatCount(e.Scrape.WordCount, "word"),
			readingTimeMinutes(e.Scrape.WordCount)))
	}

	// Language.
	if e.Scrape != nil && e.Scrape.Language != "" {
		add("Language:", escape(e.Scrape.Language))
	}

	if len(rows) == 0 {
		return ""
	}
	return `<div class="source">` + strings.Join(rows, "") + `</div>`
}

func formatCount(n int, singular string) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM %ss", float64(n)/1_000_000, singular)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK %ss", float64(n)/1_000, singular)
	case n == 1:
		return "1 " + singular
	default:
		return fmt.Sprintf("%d %ss", n, singular)
	}
}

// readingTimeMinutes estimates reading time at ~230 wpm, rounded up to 1 min min.
func readingTimeMinutes(words int) int {
	m := (words + 229) / 230
	if m < 1 {
		m = 1
	}
	return m
}

func escape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

func shortHost(u string) string {
	// strip protocol and anything past the first slash
	s := u
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "www.")
	return s
}

// voidRE matches HTML5 void elements, possibly with attributes, that need to
// be self-closed for XHTML/EPUB validity.
var voidRE = regexp.MustCompile(`(?i)<(area|base|br|col|embed|hr|img|input|link|meta|param|source|track|wbr)\b([^>]*?)\s*/?\s*>`)

// xhtmlify converts HTML5 (as produced by golang.org/x/net/html.Render and by
// readability) into something close enough to XHTML for EPUB readers.
// It's a pragmatic transform, not a full parser — go-readability has already
// sanitized structure; we just need void elements to self-close and entities
// to be escaped.
func xhtmlify(s string) string {
	// Strip control characters that are invalid in XML 1.0.
	s = stripInvalidXMLChars(s)
	// Self-close void elements.
	s = voidRE.ReplaceAllStringFunc(s, func(m string) string {
		// Extract name + attrs; rebuild as <name ... />.
		sub := voidRE.FindStringSubmatch(m)
		name, attrs := sub[1], strings.TrimSpace(sub[2])
		if attrs == "" {
			return "<" + name + "/>"
		}
		return "<" + name + " " + attrs + "/>"
	})
	// Unescaped ampersands are a common source of EPUB "not well-formed" errors.
	s = fixBareAmpersands(s)
	return s
}

// stripInvalidXMLChars removes bytes that XML 1.0 forbids
// (most control chars except \t \n \r).
func stripInvalidXMLChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 0x09 || r == 0x0A || r == 0x0D:
			b.WriteRune(r)
		case r < 0x20:
			continue
		case r >= 0x20 && r <= 0xD7FF:
			b.WriteRune(r)
		case r >= 0xE000 && r <= 0xFFFD:
			b.WriteRune(r)
		case r >= 0x10000 && r <= 0x10FFFF:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// bareAmpRE matches & that is NOT the start of a character reference.
var bareAmpRE = regexp.MustCompile(`&(?:([a-zA-Z][a-zA-Z0-9]*;)|(#[0-9]+;)|(#x[0-9a-fA-F]+;))?`)

func fixBareAmpersands(s string) string {
	return bareAmpRE.ReplaceAllStringFunc(s, func(m string) string {
		if m == "&" {
			return "&amp;"
		}
		return m
	})
}
