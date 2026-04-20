package digest

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

// RenderTXT produces a plain text file with one section per story, keeping only the text content.
func RenderTXT(runTime time.Time, entries []Entry) ([]byte, error) {
	title := fmt.Sprintf("Hacker News Top %d — %s", len(entries), runTime.UTC().Format("2006-01-02"))

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("%s\n\n", title))
	buf.WriteString(fmt.Sprintf("Generated on %s\n\n", runTime.UTC().Format("2006-01-02 15:04 UTC")))

	tagRe := regexp.MustCompile("<[^>]*>")

	for i, e := range entries {
		it := e.Item

		buf.WriteString(fmt.Sprintf("%d. %s\n\n", i+1, orFallback(it.Title, "(no title)")))

		// Build source list
		rows := buildHTMLSourceRows(e)
		for _, r := range rows {
			label := r.Label
			val := string(r.Value)
			val = tagRe.ReplaceAllString(val, "")
			val = html.UnescapeString(val)
			val = strings.TrimSpace(val)
			buf.WriteString(fmt.Sprintf("%s: %s\n", label, val))
		}
		buf.WriteString("\n")

		switch {
		case e.Err != nil:
			buf.WriteString(fmt.Sprintf("Scrape failed: %v\n\n", e.Err))
		case e.Scrape != nil && e.Scrape.Skipped != "":
			buf.WriteString(fmt.Sprintf("%s\n\n", e.Scrape.Skipped))
		case e.Scrape != nil && strings.TrimSpace(e.Scrape.Text) != "":
			buf.WriteString(strings.TrimSpace(e.Scrape.Text) + "\n\n")
		case strings.TrimSpace(it.Text) != "":
			// Replace <p> tags with double newlines
			text := strings.ReplaceAll(it.Text, "<p>", "\n\n")
			text = tagRe.ReplaceAllString(text, "")
			text = html.UnescapeString(text)
			buf.WriteString(strings.TrimSpace(text) + "\n\n")
		default:
			buf.WriteString("No extractable content.\n\n")
		}

		if i < len(entries)-1 {
			buf.WriteString("--------------------------------------------------------------------------------\n\n")
		}
	}

	return buf.Bytes(), nil
}
