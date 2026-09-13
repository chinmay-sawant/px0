package main

import (
	"bytes"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// mdDirKey carries the workspace-relative directory of the document being
// rendered into the AST transformer. The goldmark instance is shared by every
// request, so per-render state travels in the parser context instead of on a
// structure rebuilt for each call.
var mdDirKey = parser.NewContextKey()

// md renders every /api/md response. GFM adds tables, task lists, strikethrough
// and autolinks, and auto heading IDs give preview #anchors something to hit.
// The defaults are the safe ones: html.WithUnsafe is deliberately absent, so
// raw HTML is dropped and javascript:/data: destinations are emptied. Only
// fenced code gets a custom renderer, so it comes out in px0's own Chroma
// markup rather than goldmark's plain escaped text.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
		parser.WithASTTransformers(util.Prioritized(mdLinks{}, 500)),
	),
	goldmark.WithRendererOptions(
		renderer.WithNodeRenderers(util.Prioritized(mdFenceRenderer{}, 500)),
	),
)

// mdLF normalises every Markdown line ending to a bare newline. Goldmark treats
// a lone carriage return as a line ending too, but folding it up front keeps
// chroma and the mermaid source capture from ever seeing a CR.
var mdLF = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// renderMarkdown renders one Markdown document into the HTML fragment the
// preview pane inserts: no html/body wrapper, no external resources. relDir is
// the workspace-relative directory of the source file ("" at the workspace
// root, forward slashes) and is used only to resolve relative image sources.
func renderMarkdown(src []byte, relDir string) ([]byte, error) {
	if bytes.IndexByte(src, '\r') >= 0 {
		src = []byte(mdLF.Replace(string(src)))
	}
	ctx := parser.NewContext()
	ctx.Set(mdDirKey, relDir)
	var buf bytes.Buffer
	if err := md.Convert(src, &buf, parser.WithContext(ctx)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mdLinks adapts link and image destinations before rendering. Images are
// rewritten here rather than in a custom image renderer so goldmark's own
// renderer keeps doing the escaping and dangerous-URL filtering for every
// destination this transformer leaves alone.
type mdLinks struct{}

func (mdLinks) Transform(root *ast.Document, reader text.Reader, pc parser.Context) {
	relDir, _ := pc.Get(mdDirKey).(string)
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Image:
			if p, ok := mdRawPath(string(n.Destination), relDir); ok {
				n.Destination = []byte(p)
			}
		case *ast.Link:
			if mdExternal(n.Destination) {
				mdNewTab(n)
			}
		case *ast.AutoLink:
			if mdExternal(n.URL(reader.Source())) {
				mdNewTab(n)
			}
		}
		return ast.WalkContinue, nil
	})
}

// mdNewTab marks an external link so it opens away from the preview. These are
// the attribute names goldmark's link renderers already know how to print.
func mdNewTab(n ast.Node) {
	n.SetAttributeString("target", "_blank")
	n.SetAttributeString("rel", "noopener noreferrer")
}

// mdExternal reports whether an escaped destination points at a remote site.
// Only http(s) opens a new tab; mailto:, preview #anchors and in-app paths stay
// in the current page.
func mdExternal(dest []byte) bool {
	s := strings.ToLower(string(dest))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// mdRawPath resolves a relative image destination against the directory of the
// file that contains it and returns the /api/raw URL the preview should load.
// Remote, root-absolute, scheme-carrying and empty destinations are left for
// the default renderer, which keeps the safe ones and neutralises dangerous
// ones. A path that would climb out of the workspace is left alone rather than
// clamped, so a rewrite can never point above the root.
func mdRawPath(dest, relDir string) (string, bool) {
	u, err := url.Parse(dest)
	if err != nil || u.Scheme != "" || u.Path == "" || strings.HasPrefix(u.Path, "/") {
		return "", false
	}
	// Files reached through LSP-allowlisted absolute paths live outside the
	// workspace: a rewrite would resolve under the root and serve the wrong
	// file, so leave the destination alone.
	if filepath.IsAbs(filepath.FromSlash(relDir)) {
		return "", false
	}
	clean := path.Clean(path.Join(relDir, u.Path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return "/api/raw?path=" + url.QueryEscape(clean), true
}

// mdFenceRenderer renders fenced code blocks through px0's Chroma pipeline (see
// highlightSource) instead of goldmark's plain escaping. Mermaid fences are not
// lexed: the class is all the frontend needs to find them, and the source stays
// as escaped text until the diagram renderer replaces the block.
type mdFenceRenderer struct{}

func (mdFenceRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, mdRenderFence)
}

func mdRenderFence(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.FencedCodeBlock)
	lang := string(n.Language(source))
	if strings.EqualFold(lang, "mermaid") {
		lang = "mermaid" // the one spelling the frontend looks for
	}

	_, _ = w.WriteString("<pre><code")
	if lang != "" {
		_, _ = w.WriteString(` class="language-`)
		_, _ = w.Write(util.EscapeHTML([]byte(lang)))
		_ = w.WriteByte('"')
	}
	_ = w.WriteByte('>')

	code := string(n.Lines().Value(source))
	if lang == "mermaid" {
		_, _ = w.WriteString(htmlEscaper.Replace(code))
	} else {
		_, _ = w.WriteString(highlightSource(code, lang))
	}
	_, _ = w.WriteString("</code></pre>\n")
	return ast.WalkContinue, nil
}
