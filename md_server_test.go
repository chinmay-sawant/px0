package main

import (
	"bytes"
	"container/list"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// mdServerFixture carries one of everything the /api/md contract promises: an
// auto-ID heading, a GFM table, and a mermaid fence the frontend later swaps
// for a diagram.
const mdServerFixture = `# Sample Title

Intro with **bold** text.

| A | B |
| - | - |
| 1 | 2 |

` + "```go\nfunc main() {}\n```" + `

` + "```mermaid\ngraph TD; A-->B;\n```" + `
`

// mdServerWrite adds a file to a test root after newTestServer built its
// index. /api/md and /api/file resolve paths directly, so the index is not
// consulted and the late write is fine.
func mdServerWrite(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMDEndpointRendersMarkdown(t *testing.T) {
	s, root := newTestServer(t)
	mdServerWrite(t, root, "docs/guide.md", mdServerFixture)
	mdServerWrite(t, root, "docs/other.markdown", "# Other\n")

	code, body := get(t, s, "/api/md?path=docs/guide.md")
	if code != http.StatusOK {
		t.Fatalf("/api/md = %d %v", code, body)
	}
	if body["path"] != "docs/guide.md" {
		t.Errorf("path = %v, want docs/guide.md", body["path"])
	}
	if size, _ := body["size"].(float64); int(size) != len(mdServerFixture) {
		t.Errorf("size = %v, want %d", body["size"], len(mdServerFixture))
	}
	html, _ := body["html"].(string)
	for _, want := range []string{`id="sample-title"`, "<table", `class="language-mermaid"`} {
		if !strings.Contains(html, want) {
			t.Errorf("html missing %q; got:\n%s", want, html)
		}
	}

	// The other accepted suffix behaves the same.
	if code, body := get(t, s, "/api/md?path=docs/other.markdown"); code != http.StatusOK {
		t.Errorf(".markdown = %d %v, want 200", code, body)
	}

	// Preview fragments stay uncacheable like every other API response; the
	// /static/lib/ exemption is exercised separately.
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/md?path=docs/guide.md", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("/api/md Cache-Control = %q, want no-store", got)
	}
}

func TestMDEndpointRefusesBadInput(t *testing.T) {
	s, _ := newTestServer(t)
	cases := []struct {
		url  string
		want int
	}{
		{"/api/md?path=../../../etc/passwd", http.StatusBadRequest},
		{"/api/md?path=/etc/passwd", http.StatusBadRequest},
		{"/api/md?path=sub/../../outside.md", http.StatusBadRequest},
		{"/api/md?path=docs/missing.md", http.StatusNotFound},
		{"/api/md?path=main.go", http.StatusUnsupportedMediaType},
		{"/api/md?path=README.txt", http.StatusUnsupportedMediaType},
	}
	for _, c := range cases {
		code, body := get(t, s, c.url)
		if code != c.want {
			t.Errorf("%s = %d %v, want %d", c.url, code, body, c.want)
		}
		if msg, _ := body["error"].(string); msg == "" {
			t.Errorf("%s: body has no error message: %v", c.url, body)
		}
	}
}

func TestMDEndpointSizeCap(t *testing.T) {
	s, root := newTestServer(t)
	mdServerWrite(t, root, "docs/huge.md", strings.Repeat("a", 2<<20+1))

	code, body := get(t, s, "/api/md?path=docs/huge.md")
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("/api/md oversized = %d %v, want 413", code, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "large") {
		t.Errorf("413 message %q should say the file is too large", msg)
	}
}

func TestMDEndpointCacheHitAndInvalidation(t *testing.T) {
	s, root := newTestServer(t)
	p := mdServerWrite(t, root, "docs/cache.md", "# First\n\nAAA\n")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	code, body := get(t, s, "/api/md?path=docs/cache.md")
	if code != http.StatusOK {
		t.Fatalf("first render: %d %v", code, body)
	}
	first, _ := body["html"].(string)
	if first == "" {
		t.Fatal("first render returned no html")
	}

	// Same byte count, different text, mtime restored: the cache key is
	// unchanged, so the handler must serve the memoised fragment rather than
	// reread the file.
	if err := os.WriteFile(p, []byte("# First\n\nBBB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	restored, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.ModTime().Equal(st.ModTime()) {
		t.Fatalf("could not restore mtime (%v -> %v); cache key cannot be held stable", st.ModTime(), restored.ModTime())
	}
	_, body = get(t, s, "/api/md?path=docs/cache.md")
	if got, _ := body["html"].(string); got != first {
		t.Errorf("keyed fragment not reused: got %q, want the cached %q", got, first)
	}

	// A real edit changes size and mtime, so the fragment is rebuilt.
	if err := os.WriteFile(p, []byte("# Second\n\nCCC\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, body = get(t, s, "/api/md?path=docs/cache.md")
	if got, _ := body["html"].(string); got == first {
		t.Errorf("stale fragment served after an edit: %q", got)
	}

	// /api/close drops the fragment along with the highlight document.
	if code, _ := get(t, s, "/api/close?path=docs/cache.md"); code != http.StatusOK {
		t.Fatalf("/api/close = %d", code)
	}
	abs := filepath.Join(root, "docs", "cache.md")
	mdRendered.mu.Lock()
	defer mdRendered.mu.Unlock()
	for k := range mdRendered.items {
		if strings.HasPrefix(k, abs+"|") {
			t.Errorf("md cache still holds %q after /api/close", k)
		}
	}
}

// Concurrent readers share one cache entry; the responses must agree whether
// each request hit or rendered. Run under -race this also guards the LRU.
func TestMDEndpointConcurrentReads(t *testing.T) {
	s, root := newTestServer(t)
	mdServerWrite(t, root, "docs/shared.md", mdServerFixture)

	const n = 16
	results := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/md?path=docs/shared.md", nil))
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				results <- "decode error: " + err.Error()
				return
			}
			html, _ := body["html"].(string)
			results <- html
		}()
	}
	wg.Wait()
	close(results)

	var first string
	for html := range results {
		if first == "" {
			first = html
		} else if html != first {
			t.Fatalf("concurrent renders disagreed:\n%q\n%q", first, html)
		}
	}
	if first == "" {
		t.Fatal("no html came back from concurrent reads")
	}
}

// The /static/lib/ exemption must not leak onto any other route: the vendored
// tree can be cached forever, the live workspace cannot.
func TestStaticLibCacheExemption(t *testing.T) {
	// Serve a temp web tree so the check does not depend on which vendored
	// assets happen to exist yet. http.ServeContent strips Cache-Control from
	// error responses, so the exemption is only observable on a real 200.
	tmp := t.TempDir()
	for rel, body := range map[string]string{
		"web/index.html":                   "<html></html>\n",
		"web/style.css":                    "body {}\n",
		"web/lib/mermaid/mermaid.core.mjs": "export {};\n",
	} {
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := assets
	assets = os.DirFS(tmp)
	t.Cleanup(func() { assets = prev })

	s, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/lib/mermaid/mermaid.core.mjs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("vendored asset = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("/static/lib/ Cache-Control = %q, want an immutable public header", got)
	}

	// Everything else keeps the global no-store, including an API error.
	for _, u := range []string{"/", "/static/style.css", "/api/meta", "/api/md?path=missing.md"} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u, nil))
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", u, got)
		}
	}
}

// Adding /api/md must not disturb /api/file: chunks with lang for text, the
// image short-circuit for pictures, and .md still opening as highlighted
// source rather than rendered HTML.
func TestFileEndpointContractUnchanged(t *testing.T) {
	s, root := newTestServer(t)
	mdServerWrite(t, root, "docs/note.md", "# Note\n")
	mdServerWrite(t, root, "pic.png", "\x89PNG\r\n\x1a\n")

	code, body := get(t, s, "/api/file?path=greet.go")
	if code != http.StatusOK {
		t.Fatalf("/api/file greet.go = %d %v", code, body)
	}
	if body["lang"] != "Go" {
		t.Errorf("lang = %v, want Go", body["lang"])
	}
	lines, _ := body["lines"].([]any)
	if len(lines) == 0 || !strings.Contains(lines[0].(string), "class=k>package") {
		t.Errorf("highlighted lines changed: %v", body["lines"])
	}

	code, body = get(t, s, "/api/file?path=docs/note.md")
	if code != http.StatusOK {
		t.Fatalf("/api/file note.md = %d %v", code, body)
	}
	if lang, _ := body["lang"].(string); !strings.Contains(strings.ToLower(lang), "markdown") {
		t.Errorf("md lang = %v, want a markdown lexer", body["lang"])
	}
	if _, ok := body["html"]; ok {
		t.Errorf("/api/file grew an html field: %v", body)
	}

	code, body = get(t, s, "/api/file?path=pic.png")
	if code != http.StatusOK {
		t.Fatalf("/api/file pic.png = %d %v", code, body)
	}
	if body["image"] != true || body["path"] != "pic.png" {
		t.Errorf("image branch = %v", body)
	}
}

// The render cache must stay inside its budget when oversized fragments are
// added, keep the newest fragment, and drop every rendering of a closed file.
func TestMDRenderCacheBudgetEviction(t *testing.T) {
	c := &mdRenderCache{ll: list.New(), items: map[string]*list.Element{}}
	big := bytes.Repeat([]byte("x"), 20<<20) // five puts of 20 MB exceed 64 MB
	key := func(i int) string { return fmt.Sprintf("/w/f%d.md|1|%d", i, i) }
	for i := 0; i < 5; i++ {
		c.put(key(i), big)
	}
	if c.used > mdRenderCacheBudget {
		t.Fatalf("cache holds %d bytes, budget is %d", c.used, mdRenderCacheBudget)
	}
	if got := len(c.items); got != 3 {
		t.Fatalf("expected 3 surviving fragments, got %d", got)
	}
	if c.get(key(0)) != nil || c.get(key(1)) != nil {
		t.Fatal("least recently used fragments should have been evicted")
	}
	if c.get(key(2)) == nil || c.get(key(4)) == nil {
		t.Fatal("recent fragments must survive")
	}
	if !c.remove("/w/f4.md") || c.get(key(4)) != nil {
		t.Fatal("remove(abs) must drop the file's rendering")
	}
}
