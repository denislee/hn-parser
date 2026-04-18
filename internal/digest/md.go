package digest

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2"
)

// RenderMD produces a Markdown file with one section per story.
func RenderMD(runTime time.Time, entries []Entry) ([]byte, error) {
	title := fmt.Sprintf("Hacker News Top %d — %s", len(entries), runTime.UTC().Format("2006-01-02"))

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("# %s\n\n", title))
	buf.WriteString(fmt.Sprintf("Generated on %s\n\n", runTime.UTC().Format("2006-01-02 15:04 UTC")))

	for i, e := range entries {
		it := e.Item

		buf.WriteString(fmt.Sprintf("## [HN-TITLE] %d. %s\n\n", i+1, orFallback(it.Title, "(no title)")))

		// Build source table or list
		rows := buildHTMLSourceRows(e)
		for _, r := range rows {
			label := r.Label
			val, _ := htmltomarkdown.ConvertString(string(r.Value))
			val = strings.TrimSpace(val)
			buf.WriteString(fmt.Sprintf("- **%s**: %s\n", label, val))
		}
		buf.WriteString("\n")

		switch {
		case e.Err != nil:
			buf.WriteString(fmt.Sprintf("> scrape failed: %v\n\n", e.Err))
		case e.Scrape != nil && e.Scrape.Skipped != "":
			buf.WriteString(fmt.Sprintf("> %s\n\n", e.Scrape.Skipped))
		case e.Scrape != nil && strings.TrimSpace(e.Scrape.HTML) != "":
			md, err := htmltomarkdown.ConvertString(e.Scrape.HTML)
			if err != nil {
				return nil, fmt.Errorf("convert html to md: %w", err)
			}
			buf.WriteString(strings.TrimSpace(md) + "\n\n")
		case strings.TrimSpace(it.Text) != "":
			md, err := htmltomarkdown.ConvertString(it.Text)
			if err != nil {
				return nil, fmt.Errorf("convert hn text to md: %w", err)
			}
			buf.WriteString(strings.TrimSpace(md) + "\n\n")
		default:
			buf.WriteString("> no extractable content\n\n")
		}

		if i < len(entries)-1 {
			buf.WriteString("---\n\n")
		}
	}

	return buf.Bytes(), nil
}

// RenderSimplifiedMD produces a simplified Markdown file where images are replaced with '[image]' and links with '[link]'.
func RenderSimplifiedMD(runTime time.Time, entries []Entry) ([]byte, error) {
	b, err := RenderMD(runTime, entries)
	if err != nil {
		return nil, err
	}

	text := string(b)

	// Replace images first so they don't get caught as links if they are wrapped in links.
	imgRe := regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	text = imgRe.ReplaceAllString(text, "[image]")

	// Replace links
	linkRe := regexp.MustCompile(`\[[^\]]*\]\([^)]*\)`)
	text = linkRe.ReplaceAllString(text, "[link]")

	return []byte(text), nil
}
