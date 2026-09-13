package main

import (
	"net/url"
	"strings"
	"testing"
)

// mdMustRender renders a document and fails the test on error. The contract is
// an HTML fragment: no html/body wrapper and no external resources.
func mdMustRender(t *testing.T, src, relDir string) string {
	t.Helper()
	out, err := renderMarkdown([]byte(src), relDir)
	if err != nil {
		t.Fatalf("renderMarkdown(%q, relDir=%q) returned error: %v", src, relDir, err)
	}
	return string(out)
}

// mdImageSrc returns the path a single rewritten image resolves to, pulled out
// of its /api/raw URL and query-unescaped.
func mdImageSrc(t *testing.T, out string) string {
	t.Helper()
	const marker = `src="/api/raw?path=`
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("no rewritten /api/raw image in output:\n%s", out)
	}
	rest := out[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		t.Fatalf("unterminated src attribute:\n%s", out)
	}
	v, err := url.QueryUnescape(rest[:j])
	if err != nil {
		t.Fatalf("image src %q is not query-escaped cleanly: %v", rest[:j], err)
	}
	return v
}

func TestMarkdownHeadingsGetAutoIDs(t *testing.T) {
	out := mdMustRender(t, "# Hello World\n\n## Sub *Section*\n", "")
	for _, want := range []string{
		`<h1 id="hello-world">Hello World</h1>`,
		`<h2 id="sub-section">Sub <em>Section</em></h2>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestMarkdownInlineStyles(t *testing.T) {
	out := mdMustRender(t, "**bold**, *italic*, `code`, ~~gone~~ and a [link](docs/guide.md).\n", "")
	for _, want := range []string{
		"<p><strong>bold</strong>, <em>italic</em>, <code>code</code>, <del>gone</del>",
		`<a href="docs/guide.md">link</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestMarkdownGFM(t *testing.T) {
	t.Run("table", func(t *testing.T) {
		out := mdMustRender(t, "| Name | Qty |\n| ---- | --: |\n| pin | 3 |\n", "")
		for _, want := range []string{
			"<table>", "<th>Name</th>", `<th style="text-align:right">Qty</th>`,
			"<td>pin</td>", `<td style="text-align:right">3</td>`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("task list", func(t *testing.T) {
		out := mdMustRender(t, "- [x] shipped\n- [ ] pending\n", "")
		for _, want := range []string{
			`<input checked="" disabled="" type="checkbox"> shipped`,
			`<input disabled="" type="checkbox"> pending`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("strikethrough", func(t *testing.T) {
		out := mdMustRender(t, "~~gone~~\n", "")
		if !strings.Contains(out, "<del>gone</del>") {
			t.Errorf("output missing <del>:\n%s", out)
		}
	})

	t.Run("autolink", func(t *testing.T) {
		out := mdMustRender(t, "See https://example.com/a for details.\n", "")
		want := `<a href="https://example.com/a" target="_blank" rel="noopener noreferrer">https://example.com/a</a>`
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	})
}

// Fences must come out in the same <i class=..> vocabulary the file viewer
// uses; class=k is the mapping for KeywordNamespace that the /api/file tests
// already lock down.
func TestMarkdownGoFenceHighlighted(t *testing.T) {
	out := mdMustRender(t, "```go\npackage main\n\nfunc main() {}\n```\n", "")
	for _, want := range []string{
		`<pre><code class="language-go">`,
		`<i class=k>package</i> main`,
		`<i class=k>func</i> <i class=nf>main</i><i class=p>()</i>`,
		"</code></pre>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestMarkdownUnknownFenceIsEscapedText(t *testing.T) {
	out := mdMustRender(t, "```definitely-not-a-lexer\na < b && c\n```\n", "")
	if !strings.Contains(out, `<pre><code class="language-definitely-not-a-lexer">`) {
		t.Errorf("unknown fence lost its language class:\n%s", out)
	}
	if !strings.Contains(out, "a &lt; b &amp;&amp; c") {
		t.Errorf("unknown fence was not escaped plain text:\n%s", out)
	}
}

// A missing language is still a fence: escaped text, no class attribute.
func TestMarkdownBareFenceIsEscapedText(t *testing.T) {
	out := mdMustRender(t, "```\nplain <b>text</b>\n```\n", "")
	if !strings.Contains(out, "<pre><code>") {
		t.Errorf("bare fence missing <pre><code>:\n%s", out)
	}
	if !strings.Contains(out, "plain &lt;b&gt;text&lt;/b&gt;") {
		t.Errorf("bare fence was not escaped plain text:\n%s", out)
	}
	if strings.Contains(out, "language-") {
		t.Errorf("bare fence grew a language class:\n%s", out)
	}
}

// Mermaid fences are kept as escaped source, never lexed: the frontend finds
// them by class and replaces the block with a diagram.
func TestMarkdownMermaidFenceIsNotLexed(t *testing.T) {
	for _, src := range []string{
		"```mermaid\ngraph TD; A-->B;\n```\n",
		"```Mermaid\ngraph TD; A-->B;\n```\n",
	} {
		out := mdMustRender(t, src, "")
		if !strings.Contains(out, `<pre><code class="language-mermaid">`) {
			t.Errorf("mermaid fence class missing:\n%s", out)
		}
		if !strings.Contains(out, "graph TD; A--&gt;B;") {
			t.Errorf("mermaid source was not preserved as escaped text:\n%s", out)
		}
		if strings.Contains(out, "<i ") {
			t.Errorf("mermaid fence was chroma-lexed:\n%s", out)
		}
	}
}

func TestMarkdownRawHTMLOmitted(t *testing.T) {
	src := `<script>alert(1)</script>

<img src=x onerror=alert(1)>

<iframe src="https://example.invalid/"></iframe>
`
	out := mdMustRender(t, src, "")
	for _, forbidden := range []string{"<script", "</script>", "<img", "onerror", "<iframe"} {
		if strings.Contains(strings.ToLower(out), forbidden) {
			t.Errorf("raw HTML %q survived rendering:\n%s", forbidden, out)
		}
	}
}

func TestMarkdownDangerousLinksRejected(t *testing.T) {
	out := mdMustRender(t, "[js](javascript:alert(1)) and [data](data:text/html;base64,PHNjcmlwdD4=)\n", "")
	low := strings.ToLower(out)
	for _, forbidden := range []string{"javascript:", "data:text/html"} {
		if strings.Contains(low, forbidden) {
			t.Errorf("dangerous destination %q survived rendering:\n%s", forbidden, out)
		}
	}
	if got := strings.Count(out, `href=""`); got != 2 {
		t.Errorf("want both hrefs emptied, got %d in:\n%s", got, out)
	}
}

func TestMarkdownExternalLinksOpenNewTab(t *testing.T) {
	out := mdMustRender(t, "[site](https://example.com/page) [local](docs/guide.md) [top](#heading)\n", "")
	for _, want := range []string{
		`<a href="https://example.com/page" target="_blank" rel="noopener noreferrer">site</a>`,
		`<a href="docs/guide.md">local</a>`,
		`<a href="#heading">top</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, `target="_blank"`); got != 1 {
		t.Errorf("only the http(s) link may open a new tab; got %d targets in:\n%s", got, out)
	}
}

func TestMarkdownRelativeImagesRewritten(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		relDir string
		want   string
	}{
		{"workspace root", "![p](img/px.png)\n", "", "img/px.png"},
		{"same directory", "![p](img/px.png)\n", "docs/api", "docs/api/img/px.png"},
		{"parent directory", "![p](../img/px.png)\n", "docs/api", "docs/img/px.png"},
		{"encoded space", "![p](my%20images/px.png)\n", "docs", "docs/my images/px.png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mdMustRender(t, tc.src, tc.relDir)
			if got := mdImageSrc(t, out); got != tc.want {
				t.Errorf("image resolves to %q, want %q\noutput:\n%s", got, tc.want, out)
			}
		})
	}
}

// A path that would leave the workspace is not clamped to a wrong file: it is
// simply left for the browser to fail on, same as any other broken image.
func TestMarkdownEscapingImagesNotRewritten(t *testing.T) {
	out := mdMustRender(t, "![p](../../../etc/passwd.png)\n", "docs")
	if strings.Contains(out, "/api/raw") {
		t.Errorf("escaping image was rewritten:\n%s", out)
	}
	if !strings.Contains(out, `src="../../../etc/passwd.png"`) {
		t.Errorf("escaping image source was altered:\n%s", out)
	}
}

func TestMarkdownRemoteImageUnchanged(t *testing.T) {
	out := mdMustRender(t, "![p](https://example.com/a.png)\n", "docs")
	if !strings.Contains(out, `src="https://example.com/a.png"`) {
		t.Errorf("remote image source changed:\n%s", out)
	}
	if strings.Contains(out, "/api/raw") {
		t.Errorf("remote image was sent through /api/raw:\n%s", out)
	}
}

// Files reached through LSP-allowlisted absolute paths live outside the
// workspace: rewriting their images would resolve under the root and serve the
// wrong file, so the destination is left untouched.
func TestMarkdownAbsoluteDirLeavesImagesUntouched(t *testing.T) {
	out := mdMustRender(t, "![p](img/px.png)\n", "/tmp/outside")
	if strings.Contains(out, "/api/raw") {
		t.Errorf("image under an absolute directory was rewritten:\n%s", out)
	}
	if !strings.Contains(out, `src="img/px.png"`) {
		t.Errorf("image source was altered:\n%s", out)
	}
}

func TestMarkdownEmptyInput(t *testing.T) {
	for _, src := range []string{"", "\n", "\n\n"} {
		out, err := renderMarkdown([]byte(src), "")
		if err != nil {
			t.Fatalf("renderMarkdown(%q) returned error: %v", src, err)
		}
		if strings.TrimSpace(string(out)) != "" {
			t.Errorf("empty input produced %q", out)
		}
	}
}

func TestMarkdownCRLFInput(t *testing.T) {
	out := mdMustRender(t, "# CRLF Title\r\n\r\nBody with **bold** text.\r\n", "")
	if strings.Contains(out, "\r") {
		t.Errorf("carriage return leaked into rendered output: %q", out)
	}
	for _, want := range []string{
		`<h1 id="crlf-title">CRLF Title</h1>`,
		"<p>Body with <strong>bold</strong> text.</p>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
