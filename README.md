# hn-parser

A Go CLI that builds a readable digest of the current Hacker News top
stories — as both a single-page HTML document and an EPUB — and
publishes them to the `hn/` folder of
[denislee.github.io](https://github.com/denislee/denislee.github.io), so
the result is browsable at <https://denislee.github.io/hn/>.

For each top story it:

1. Fetches the item metadata from the HN Firebase API.
2. Fetches the linked article and runs it through
   [go-readability](https://codeberg.org/readeck/go-readability) to
   extract the cleaned article content (Mozilla Readability port).
3. Renders two files:
   - `hn/YYYY-MM-DD.html` — single styled HTML page with all stories.
   - `hn/YYYY-MM-DD.epub` — EPUB with one chapter per story, built via
     [go-epub](https://github.com/go-shiori/go-epub).
4. Regenerates `hn/index.html`, one row per date with HTML + EPUB links.
5. Commits and pushes the target repo on `master`.

Ask HN / Show HN posts with no external URL use the item's own text.
Binary URLs (PDFs, images, videos…) and non-HTML responses are recorded
as a short stub rather than scraped.

## Build

```sh
go build -o hn-parser .
```

Requires Go 1.22+ and the `git` CLI (the app shells out to `git` so it
reuses your existing SSH credentials).

## Usage

```sh
./hn-parser [flags]
```

| Flag          | Default                                              | Description                                                                 |
| ------------- | ---------------------------------------------------- | --------------------------------------------------------------------------- |
| `-n`          | `30`                                                 | Number of HN top stories to include.                                        |
| `-c`          | `6`                                                  | Parallel scrape workers.                                                    |
| `-timeout`    | `30s`                                                | Per-URL scrape timeout.                                                     |
| `-target`     | `$HOME/src/denislee.github.io`                       | Local working clone of the target repo. Cloned on first run if missing.     |
| `-remote`     | `git@github.com:denislee/denislee.github.io.git`     | Git remote URL. Only used if `-target` doesn't exist yet.                   |
| `-branch`     | `master`                                             | Branch to commit / push on the target repo.                                 |
| `-subdir`     | `hn`                                                 | Subdirectory inside the target repo to publish into.                        |
| `-dry-run`    | `false`                                              | Write files locally but don't commit or push.                               |
| `-no-push`    | `false`                                              | Commit locally but don't push.                                              |

### First run (recommended)

Do a dry run into a throwaway directory to check the output looks right:

```sh
./hn-parser -n 5 --dry-run -target /tmp/ghpages-test
xdg-open /tmp/ghpages-test/hn/$(date -u +%F).html    # or just open in a browser
```

Then a local-only commit into your real clone:

```sh
./hn-parser --no-push
git -C ~/src/denislee.github.io show HEAD
```

Finally the real thing:

```sh
./hn-parser
```

GitHub Pages usually publishes ~1 min after push.

### Cron example

```cron
# Update the HN digest every 6 hours.
15 */6 * * * cd $HOME && $HOME/bin/hn-parser >> $HOME/.hn-parser.log 2>&1
```

## Notes

- Same-day re-runs overwrite both `hn/YYYY-MM-DD.html` and
  `hn/YYYY-MM-DD.epub` and produce a second commit (or none if both
  files are byte-identical).
- The app only stages files inside `-subdir`, so unrelated changes in
  your working clone are never swept into a digest commit.
- The User-Agent on all outgoing requests is
  `hn-parser/1.0 (+https://github.com/denislee/hn-parser)`.
