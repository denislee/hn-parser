// Package scrape fetches a URL and returns the readable plain-text body
// using go-readability (a Go port of Mozilla's Readability.js).
//
// Binary / non-HTML responses are detected up front (Content-Type + URL
// extension) and returned as a clearly-labelled stub rather than run
// through the HTML parser.
package scrape

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
)

// Result is what we extracted from one URL.
type Result struct {
	FinalURL      string    // URL after redirects
	Title         string    // Readability-extracted title
	Byline        string    // Author, if detected
	SiteName      string    // e.g. "GitHub", "The New York Times"
	Excerpt       string    // Short summary / first paragraph
	Language      string    // Detected language code ("en", "pt", …)
	PublishedTime time.Time // Zero if not detected
	WordCount     int       // Approximate word count of the extracted body
	HTML          string    // Cleaned HTML body (may be empty on failure). Already sanitized by readability.
	Text          string    // Cleaned plain text body.
	Skipped       string    // Non-empty if we deliberately skipped (e.g. PDF)
}

// Scraper is a reusable fetcher. The zero value is not usable; use New.
type Scraper struct {
	http         *http.Client
	httpInsecure *http.Client
	ua           string
	maxSize      int64
}

// New returns a Scraper. perRequestTimeout is applied via context on each
// Fetch call; the underlying http.Client timeout should be larger or zero
// so Scraper can cancel cleanly via the context.
func New(ua string, perRequestTimeout time.Duration) *Scraper {
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	clientTimeout := perRequestTimeout + 10*time.Second
	insecureTransport := http.DefaultTransport.(*http.Transport).Clone()
	insecureTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- fallback for broken certs on content scraping
	return &Scraper{
		http: &http.Client{
			Timeout:       clientTimeout,
			CheckRedirect: checkRedirect,
		},
		httpInsecure: &http.Client{
			Timeout:       clientTimeout,
			CheckRedirect: checkRedirect,
			Transport:     insecureTransport,
		},
		ua:      ua,
		maxSize: 8 << 20, // 8 MiB cap on response body
	}
}

// isTLSCertError returns true if err is a TLS certificate verification failure
// (expired, self-signed, unknown authority, hostname mismatch, etc.).
func isTLSCertError(err error) bool {
	if err == nil {
		return false
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidErr x509.CertificateInvalidError
	var tlsRecordErr *tls.RecordHeaderError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostnameErr) ||
		errors.As(err, &invalidErr) || errors.As(err, &tlsRecordErr) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "x509:") || strings.Contains(msg, "tls: failed to verify certificate")
}

// binaryExts are URL path suffixes we won't even try to read.
var binaryExts = map[string]bool{
	".pdf": true, ".epub": true, ".zip": true, ".tar": true, ".gz": true,
	".rar": true, ".7z": true, ".exe": true, ".dmg": true, ".iso": true,
	".mp3": true, ".mp4": true, ".webm": true, ".mov": true, ".avi": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".svg": true, ".ico": true, ".mkv": true, ".wav": true, ".flac": true,
}

// Fetch retrieves pageURL and returns a readable representation.
// Non-HTML responses are returned with a populated Skipped field.
func (s *Scraper) Fetch(ctx context.Context, pageURL string, timeout time.Duration) (*Result, error) {
	u, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if ext := strings.ToLower(path.Ext(u.Path)); binaryExts[ext] {
		return &Result{
			FinalURL: pageURL,
			Skipped:  fmt.Sprintf("binary file (%s); not scraped", ext),
		}, nil
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := s.http.Do(req)
	if err != nil && isTLSCertError(err) {
		log.Printf("scrape: TLS cert invalid for %s (%v); retrying without verification", pageURL, err)
		retryReq, rerr := http.NewRequestWithContext(reqCtx, http.MethodGet, pageURL, nil)
		if rerr == nil {
			retryReq.Header = req.Header.Clone()
			resp, err = s.httpInsecure.Do(retryReq)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "text/html") && !strings.Contains(ct, "application/xhtml") {
		return &Result{
			FinalURL: resp.Request.URL.String(),
			Skipped:  fmt.Sprintf("non-HTML content-type (%s); not scraped", ct),
		}, nil
	}

	body := io.LimitReader(resp.Body, s.maxSize)

	article, err := readability.FromReader(body, resp.Request.URL)
	if err != nil {
		return nil, fmt.Errorf("readability: %w", err)
	}

	var (
		htmlOut   string
		textOut   string
		wordCount int
		excerpt   string
	)
	if article.Node != nil {
		var sb strings.Builder
		if err := article.RenderHTML(&sb); err != nil {
			return nil, fmt.Errorf("render: %w", err)
		}
		htmlOut = sb.String()

		var tb strings.Builder
		if err := article.RenderText(&tb); err == nil {
			textOut = tb.String()
			wordCount = len(strings.Fields(textOut))
		}
		// Excerpt() walks article.Node internally, so we must only call it
		// after we've confirmed Node is non-nil; otherwise the library panics.
		excerpt = strings.TrimSpace(article.Excerpt())
	}

	var published time.Time
	if t, err := article.PublishedTime(); err == nil {
		published = t
	}

	return &Result{
		FinalURL:      resp.Request.URL.String(),
		Title:         strings.TrimSpace(article.Title()),
		Byline:        strings.TrimSpace(article.Byline()),
		SiteName:      strings.TrimSpace(article.SiteName()),
		Excerpt:       excerpt,
		Language:      strings.TrimSpace(article.Language()),
		PublishedTime: published,
		WordCount:     wordCount,
		HTML:          htmlOut,
		Text:          textOut,
	}, nil
}
