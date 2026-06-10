package digest

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The .rsvp format is the tiny line-based text format read by the RSVP Nano
// ESP32 reader and produced by its browser converter at
// https://ionutdecebal.github.io/rsvpnano/. Layout:
//
//	@rsvp 1
//	@title <document title>
//	@author <author>        (optional)
//	@source <source name>
//
//	@chapter <chapter title>
//	<word-wrapped text lines>
//	                        (blank line == paragraph break)
//	<more text>
//	@chapter <next chapter>
//	...
//
// Rules matched from the official converters / firmware parser:
//   - directive values are single-line (whitespace collapsed)
//   - body lines are wrapped on word boundaries to <= WRAP_WIDTH columns,
//     never breaking a word
//   - a text line that would start with '@' is escaped to '@@' so the parser
//     reads it as literal text instead of a directive
//   - unknown directives (e.g. @rsvp, @source) are silently ignored by the
//     firmware, blank lines act as paragraph breaks.
const (
	rsvpVersion   = "1"
	rsvpWrapWidth = 96
)

var (
	rsvpTagRe = regexp.MustCompile("<[^>]*>")
	rsvpWSRe  = regexp.MustCompile(`\s+`)
	rsvpNLRe  = regexp.MustCompile(`\n+`)
)

// RenderRSVP produces a .rsvp file: one HN story per @chapter, with the article
// body broken into paragraphs.
func RenderRSVP(runTime time.Time, entries []Entry) ([]byte, error) {
	title := fmt.Sprintf("Hacker News Top %d — %s", len(entries), runTime.UTC().Format("2006-01-02"))

	w := &rsvpWriter{}
	w.lines = append(w.lines,
		"@rsvp "+rsvpVersion,
		"@title "+rsvpDirective(title),
		"@author hn-parser",
		"@source "+runTime.UTC().Format("2006-01-02")+".rsvp",
		"",
	)

	for i, e := range entries {
		it := e.Item
		w.chapter(fmt.Sprintf("%d. %s", i+1, orFallback(it.Title, "(no title)")))

		// Source metadata as the chapter's opening paragraph.
		var meta []string
		for _, r := range buildHTMLSourceRows(e) {
			val := strings.TrimSpace(html.UnescapeString(rsvpTagRe.ReplaceAllString(string(r.Value), "")))
			if val != "" {
				meta = append(meta, r.Label+": "+val)
			}
		}
		if len(meta) > 0 {
			w.paragraph(strings.Join(meta, " · "))
		}

		switch {
		case e.Err != nil:
			w.paragraph(fmt.Sprintf("Scrape failed: %v", e.Err))
		case e.Scrape != nil && e.Scrape.Skipped != "":
			w.paragraph(e.Scrape.Skipped)
		case e.Scrape != nil && strings.TrimSpace(e.Scrape.Text) != "":
			for _, p := range rsvpParagraphs(e.Scrape.Text) {
				w.paragraph(p)
			}
		case strings.TrimSpace(it.Text) != "":
			text := strings.ReplaceAll(it.Text, "<p>", "\n\n")
			text = rsvpTagRe.ReplaceAllString(text, "")
			text = html.UnescapeString(text)
			for _, p := range rsvpParagraphs(text) {
				w.paragraph(p)
			}
		default:
			w.paragraph("No extractable content.")
		}
	}

	return []byte(w.finalize()), nil
}

// rsvpWriter accumulates lines, wrapping body text to rsvpWrapWidth columns.
type rsvpWriter struct {
	lines     []string
	lineWords []string
	lineLen   int // rune length of the current line being built
}

func (w *rsvpWriter) chapter(title string) {
	w.flushLine()
	w.blankLine()
	w.lines = append(w.lines, "@chapter "+rsvpDirective(title))
}

// paragraph emits text as its own paragraph, separated from the previous block
// by a blank line (which the firmware parser treats as a paragraph break).
func (w *rsvpWriter) paragraph(text string) {
	w.flushLine()
	w.blankLine()
	w.addText(text)
	w.flushLine()
}

func (w *rsvpWriter) addText(text string) {
	for _, word := range strings.Fields(text) {
		wl := utf8.RuneCountInString(word)
		if len(w.lineWords) > 0 && w.lineLen+1+wl > rsvpWrapWidth {
			w.flushLine()
		}
		if len(w.lineWords) == 0 {
			w.lineLen = wl
		} else {
			w.lineLen += 1 + wl
		}
		w.lineWords = append(w.lineWords, word)
	}
}

func (w *rsvpWriter) flushLine() {
	if len(w.lineWords) == 0 {
		return
	}
	line := strings.Join(w.lineWords, " ")
	if strings.HasPrefix(line, "@") {
		// Escape so the parser reads it as text, not a directive.
		line = "@" + line
	}
	w.lines = append(w.lines, line)
	w.lineWords = w.lineWords[:0]
	w.lineLen = 0
}

// blankLine appends a single blank line, collapsing consecutive blanks.
func (w *rsvpWriter) blankLine() {
	if len(w.lines) > 0 && w.lines[len(w.lines)-1] != "" {
		w.lines = append(w.lines, "")
	}
}

func (w *rsvpWriter) finalize() string {
	w.flushLine()
	return strings.TrimSpace(strings.Join(w.lines, "\n")) + "\n"
}

// rsvpDirective collapses a value to a single trimmed line.
func rsvpDirective(s string) string {
	return strings.TrimSpace(rsvpWSRe.ReplaceAllString(s, " "))
}

// rsvpParagraphs splits plain text into non-empty paragraphs on newline runs.
func rsvpParagraphs(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var out []string
	for _, p := range rsvpNLRe.Split(text, -1) {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}
