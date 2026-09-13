package main

// md_security_test.go is the independent adversarial corpus for renderMarkdown.
// It is deliberately separate from md_test.go: these are the XSS and path
// containment cases called out by the canonical Markdown preview plan
// (00-canonical-markdown-preview rows 1.3, 1.4.2 and 5.1).
//
// Contract under test, for renderMarkdown(src []byte, relDir string) ([]byte, error):
//   - raw HTML (<script>, <img onerror>, <svg onload>, <iframe>, comments and
//     malformed variants) is never passed through;
//   - javascript: and data: destinations are neutralized;
//   - fenced code, including a fenced "html" block, is escaped text;
//   - relative image sources are rewritten to /api/raw?path=<resolved> and the
//     resolved path can never climb above the workspace root;
//   - empty and CRLF input neither error nor emit unsafe output.

import (
	"net/url"
	"path"
	"regexp"
	"strings"
	"testing"
)

func secMustRender(t *testing.T, src, relDir string) string {
	t.Helper()
	out, err := renderMarkdown([]byte(src), relDir)
	if err != nil {
		t.Fatalf("renderMarkdown(%q, relDir=%q) returned error: %v", src, relDir, err)
	}
	return string(out)
}

// secMustNotContain fails when any forbidden substring reaches the output. The
// comparison is case-insensitive because HTML tag names are.
func secMustNotContain(t *testing.T, out string, forbidden ...string) {
	t.Helper()
	low := strings.ToLower(out)
	for _, f := range forbidden {
		if strings.Contains(low, strings.ToLower(f)) {
			t.Errorf("dangerous substring %q survived rendering:\n%s", f, out)
		}
	}
}

func TestRenderMarkdownSecurity_XSSCorpus(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		forbid []string
	}{
		{"script block", `<script>alert(1)</script>`, []string{"<script", "</script>"}},
		{"script in paragraph", `text before <script>alert(1)</script> text after`, []string{"<script", "</script>"}},
		{"script in inline code", "`<script>alert(1)</script>`", []string{"<script", "</script>"}},
		{"img onerror", `<img src=x onerror=alert(1)>`, []string{"<img"}},
		{"svg onload", `<svg onload=alert(1)></svg>`, []string{"<svg"}},
		{"iframe", `<iframe src="https://example.invalid/"></iframe>`, []string{"<iframe"}},
		{"javascript link", `[click me](javascript:alert(1))`, []string{"javascript:"}},
		{"mixed-case javascript link", `[click me](JaVaScRiPt:alert(1))`, []string{"javascript:"}},
		{"data text/html link", `[click me](data:text/html;base64,PHNjcmlwdD4=)`, []string{"data:text/html"}},
		{"html comment hiding script", "<!-- <script>alert(1)</script> -->", []string{"<script", "</script>"}},
		{"javascript image source", `![pixel](javascript:alert(1))`, []string{"javascript:"}},
		{"data image source", `![pixel](data:text/html;base64,PHNjcmlwdD4=)`, []string{"data:text/html"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := secMustRender(t, tc.src, "")
			secMustNotContain(t, out, tc.forbid...)
		})
	}
}

func TestRenderMarkdownSecurity_MalformedHTMLPassthroughIsSafe(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		forbid []string
	}{
		{"unclosed script", `<script>alert(1)`, []string{"<script"}},
		{"unclosed iframe", `<iframe src="x">oops`, []string{"<iframe"}},
		{"unclosed div with handler", `<div onclick=alert(1)>click`, []string{"<div"}},
		{"stray closing tags", `</div></script><img src=x onerror=alert(1)>`, []string{"<div", "<img", "</script"}},
		{"malformed link destination", `[oops](<javascript:alert(1)>`, []string{`href="javascript:`}},
		{"quote soup image", `<img src="x" onerror="alert('1')">`, []string{"<img"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := secMustRender(t, tc.src, "")
			secMustNotContain(t, out, tc.forbid...)
		})
	}
}

// A fenced html block must surface its tags as escaped text, never as live
// markup. Chroma may split "<script>" across token spans, so the assertions
// only require that the raw tag is gone and that the escaped form/prose is
// still present.
func TestRenderMarkdownSecurity_FencedHTMLIsEscaped(t *testing.T) {
	src := "```html\n<script>alert(1)</script>\n<img src=x onerror=alert(1)>\n```\n"
	out := secMustRender(t, src, "")
	secMustNotContain(t, out, "<script", "</script>", "<img")
	if !strings.Contains(out, "&lt;") {
		t.Errorf("fenced html was not HTML-escaped:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "script") {
		t.Errorf("fenced html content disappeared instead of rendering as escaped text:\n%s", out)
	}
}

var secRawPathValueRe = regexp.MustCompile(`/api/raw\?path=([^"'&<> \t\r\n]*)`)

// secRawPaths returns every /api/raw?path= value in out, query-unescaped once.
func secRawPaths(t *testing.T, out string) []string {
	t.Helper()
	var paths []string
	for _, m := range secRawPathValueRe.FindAllStringSubmatch(out, -1) {
		v, err := url.QueryUnescape(m[1])
		if err != nil {
			t.Errorf("unparseable /api/raw?path value %q: %v", m[1], err)
			v = m[1]
		}
		paths = append(paths, v)
	}
	return paths
}

// secEscapesRoot reports whether a rewritten /api/raw path leaves the workspace
// root once cleaned: a leading ".." segment or an absolute path.
func secEscapesRoot(p string) bool {
	clean := path.Clean(p)
	return clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean)
}

func TestRenderMarkdownSecurity_RelativeImageRewritesStayUnderRoot(t *testing.T) {
	out := secMustRender(t, "![pixel](img/px.png)\n", "docs/sub")
	got := secRawPaths(t, out)
	if len(got) != 1 {
		t.Fatalf("want exactly one /api/raw image rewrite, got %d: %v\noutput:\n%s", len(got), got, out)
	}
	if clean := path.Clean(got[0]); clean != "docs/sub/img/px.png" {
		t.Errorf("rewritten image path = %q (clean %q), want docs/sub/img/px.png\noutput:\n%s", got[0], clean, out)
	}
}

func TestRenderMarkdownSecurity_ImageTraversalCannotEscapeRoot(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		relDir string
	}{
		{"dots climb above root", "![p](../../../../etc/passwd.png)", "docs/sub"},
		{"relDir above root", "![p](safe.png)", "../../.."},
		{"encoded dots", "![p](%2e%2e/%2e%2e/etc/passwd.png)", "docs"},
		{"dots stay inside root", "![p](../img/px.png)", "docs/sub"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := secMustRender(t, tc.src, tc.relDir)
			for _, p := range secRawPaths(t, out) {
				if secEscapesRoot(p) {
					t.Errorf("rewritten /api/raw?path=%q escapes the workspace root (relDir=%q)\noutput:\n%s", p, tc.relDir, out)
				}
			}
		})
	}
}

func TestRenderMarkdownSecurity_EmptyInputIsSafe(t *testing.T) {
	for _, src := range [][]byte{nil, {}, []byte("\n\n")} {
		out, err := renderMarkdown(src, "")
		if err != nil {
			t.Fatalf("renderMarkdown(%q) returned error: %v", src, err)
		}
		if strings.TrimSpace(string(out)) != "" {
			t.Errorf("empty input produced output %q", out)
		}
	}
}

func TestRenderMarkdownSecurity_CRLFInputIsSafe(t *testing.T) {
	out := secMustRender(t, "# CRLF Title\r\n\r\nBody\r\n", "")
	if strings.Contains(out, "\r") {
		t.Errorf("carriage return leaked into rendered output: %q", out)
	}
	if !strings.Contains(out, "<h1") {
		t.Errorf("CRLF heading did not render:\n%s", out)
	}
	if !strings.Contains(out, `id="crlf-title"`) {
		t.Errorf("CRLF heading lost its auto id:\n%s", out)
	}
}
