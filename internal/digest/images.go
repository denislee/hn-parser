package digest

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	epub "github.com/go-shiori/go-epub"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	imgMaxWidth     = 600 // px – fits most e-readers
	imgMaxHeight    = 800 // px
	jpegQuality     = 55  // compact but readable
	imgFetchTimeout = 10 * time.Second
	maxImgBytes     = 5 << 20 // 5 MiB download cap (resized+compressed on output)
)

// imgProcessor downloads, resizes, and embeds images into an EPUB book or
// converts them to data URLs for standalone HTML.
type imgProcessor struct {
	client  *http.Client
	book    *epub.Epub
	tempDir string
	cache   map[string]string // srcURL → EPUB internal path or data URL
	counter int
}

func newImgProcessor(book *epub.Epub) (*imgProcessor, error) {
	dir, err := os.MkdirTemp("", "hn-digest-img-*")
	if err != nil {
		return nil, err
	}
	return &imgProcessor{
		client:  &http.Client{Timeout: 15 * time.Second},
		book:    book,
		tempDir: dir,
		cache:   make(map[string]string),
	}, nil
}

func (p *imgProcessor) close() {
	os.RemoveAll(p.tempDir)
}

// imgSrcRE matches <img ... src="URL" ...> and captures the three parts so
// we can replace just the URL while preserving the rest of the tag.
var imgSrcRE = regexp.MustCompile(`(?i)(<img\b[^>]*?\bsrc\s*=\s*")([^"]+)("[^>]*/?>)`)

// ProcessHTML finds all <img> tags, downloads/resizes their sources, embeds
// them (either in the EPUB or as data URLs), and returns updated HTML.
// Images that fail to download or decode are silently removed.
// baseURL is used to resolve relative image paths.
func (p *imgProcessor) ProcessHTML(html string, baseURL string) string {
	var base *url.URL
	if baseURL != "" {
		base, _ = url.Parse(baseURL)
	}

	return imgSrcRE.ReplaceAllStringFunc(html, func(match string) string {
		sub := imgSrcRE.FindStringSubmatch(match)
		if len(sub) < 4 {
			return match
		}
		prefix, srcURL, suffix := sub[1], sub[2], sub[3]

		if strings.HasPrefix(srcURL, "data:") {
			return match
		}

		if base != nil {
			if u, err := url.Parse(srcURL); err == nil && !u.IsAbs() {
				srcURL = base.ResolveReference(u).String()
			}
		}

		newSrc, err := p.embed(srcURL)
		if err != nil {
			log.Printf("  image skip: %s: %v", truncURL(srcURL), err)
			return "" // drop broken image tag
		}
		return prefix + newSrc + suffix
	})
}

func (p *imgProcessor) embed(srcURL string) (string, error) {
	if ep, ok := p.cache[srcURL]; ok {
		return ep, nil
	}
	p.counter++
	n := p.counter

	ctx, cancel := context.WithTimeout(context.Background(), imgFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hn-parser/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}

	// Read full body so we can inspect format before decoding.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImgBytes))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	ct := resp.Header.Get("Content-Type")

	// SVG: embed directly or as data URL.
	if isSVG(srcURL, ct, data) {
		var newSrc string
		var err error
		if p.book != nil {
			newSrc, err = p.embedSVG(data, n)
		} else {
			newSrc = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(data)
		}
		if err != nil {
			return "", err
		}
		p.cache[srcURL] = newSrc
		return newSrc, nil
	}

	// Raster image: decode → resize → JPEG.
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	// Skip tiny images (tracking pixels, spacers).
	b := img.Bounds()
	if b.Dx() < 10 || b.Dy() < 10 {
		return "", fmt.Errorf("too small (%dx%d)", b.Dx(), b.Dy())
	}

	img = fitImage(img, imgMaxWidth, imgMaxHeight)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return "", err
	}

	var newSrc string
	if p.book != nil {
		fname := fmt.Sprintf("img-%04d.jpg", n)
		tmp := filepath.Join(p.tempDir, fname)
		if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
			return "", err
		}
		newSrc, err = p.book.AddImage(tmp, fname)
		if err != nil {
			return "", fmt.Errorf("add to epub: %w", err)
		}
	} else {
		newSrc = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	}

	p.cache[srcURL] = newSrc
	return newSrc, nil
}

// isSVG returns true if the response looks like an SVG image.
func isSVG(srcURL, contentType string, data []byte) bool {
	if strings.Contains(contentType, "image/svg") {
		return true
	}
	if strings.HasSuffix(strings.ToLower(srcURL), ".svg") {
		return true
	}
	// Peek at the content: look for <svg within the first 256 bytes
	// (handles both raw SVG and <?xml ...> prologues).
	peek := data
	if len(peek) > 256 {
		peek = peek[:256]
	}
	return bytes.Contains(bytes.ToLower(peek), []byte("<svg"))
}

// embedSVG writes SVG data to a temp file and adds it to the EPUB as-is.
func (p *imgProcessor) embedSVG(data []byte, n int) (string, error) {
	fname := fmt.Sprintf("img-%04d.svg", n)
	tmp := filepath.Join(p.tempDir, fname)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}

	epubPath, err := p.book.AddImage(tmp, fname)
	if err != nil {
		return "", fmt.Errorf("add svg to epub: %w", err)
	}
	return epubPath, nil
}

// fitImage downscales img to fit within maxW×maxH, preserving aspect ratio.
// Returns the original if it already fits.
func fitImage(img image.Image, maxW, maxH int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW && h <= maxH {
		return img
	}

	scale := float64(maxW) / float64(w)
	if s := float64(maxH) / float64(h); s < scale {
		scale = s
	}
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

func truncURL(u string) string {
	if len(u) <= 80 {
		return u
	}
	return u[:77] + "..."
}
