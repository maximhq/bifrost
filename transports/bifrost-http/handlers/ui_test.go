package handlers

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
)

const uiTestShell = `<!doctype html>
<html lang="en">
	<head>
		<link rel="icon" type="image/x-icon" href="/favicon.ico" />
		<script>var up = "<img class=l src=/bifrost-logo.webp>";</script>
		<script type="module" crossorigin src="/assets/index-abc.js"></script>
		<link rel="modulepreload" crossorigin href="/assets/chunk-def.js">
	</head>
	<body><a href="https://getbifrost.ai">Bifrost</a><a href="//cdn.example">cdn</a><img src="/bifrost-logo.webp" /></body>
</html>`

// uiTestFS returns an in-memory UI bundle: the uiTestShell page, a JS asset and
// a favicon.
func uiTestFS() fstest.MapFS {
	return fstest.MapFS{
		"ui/index.html":          {Data: []byte(uiTestShell)},
		"ui/assets/index-abc.js": {Data: []byte("console.log(1)")},
		"ui/favicon.ico":         {Data: []byte("ico")},
	}
}

// serveUITest sends a GET for path through the handler's dashboard route and
// returns the request context holding the response.
func serveUITest(h *UIHandler, path string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI(path)
	h.serveDashboard(ctx)
	return ctx
}

// TestUIHandler_NoBasePathServesShellUnchanged pins the default: the shell is
// served byte-for-byte as embedded and no rewritten copies are kept.
func TestUIHandler_NoBasePathServesShellUnchanged(t *testing.T) {
	h := NewUIHandler(uiTestFS(), nil, "")
	assert.Nil(t, h.basePathShells)
	for _, path := range []string{"/", "/workspace/logs"} {
		ctx := serveUITest(h, path)
		assert.Equal(t, uiTestShell, string(ctx.Response.Body()), path)
	}
}

// TestUIHandler_BasePathRewritesShell checks root-absolute src/href refs gain the
// prefix (quoted and unquoted), external and protocol-relative URLs do not, and
// the runtime global is set at the top of <head>, for the root and SPA routes.
func TestUIHandler_BasePathRewritesShell(t *testing.T) {
	h := NewUIHandler(uiTestFS(), nil, "/tools/llm-gw")
	for _, path := range []string{"/", "/workspace/logs", "/login"} {
		body := string(serveUITest(h, path).Response.Body())
		assert.Contains(t, body, `<head><script>window.__BIFROST_BASE_PATH__="/tools/llm-gw"</script>`, path)
		assert.Contains(t, body, `href="/tools/llm-gw/favicon.ico"`, path)
		assert.Contains(t, body, `src=/tools/llm-gw/bifrost-logo.webp>`, path)
		assert.Contains(t, body, `src="/tools/llm-gw/assets/index-abc.js"`, path)
		assert.Contains(t, body, `href="/tools/llm-gw/assets/chunk-def.js"`, path)
		assert.Contains(t, body, `<img src="/tools/llm-gw/bifrost-logo.webp" />`, path)
		assert.Contains(t, body, `href="https://getbifrost.ai"`, path)
		assert.Contains(t, body, `href="//cdn.example"`, path)
		assert.NotContains(t, body, `"/assets/`, path)
	}
	// Static assets are served untouched; the prefix is stripped before routing.
	assert.Equal(t, "console.log(1)", string(serveUITest(h, "/assets/index-abc.js").Response.Body()))
}

// TestUIHandler_NonCanonicalBasePathNotSpliced pins that the shell rewrite
// only splices a value lib.NormalizeBasePath returns unchanged. Anything else
// (markup, a trailing slash, a missing leading slash) is refused and the shell
// is served as embedded, so a caller that skips normalization cannot inject
// into the HTML or the inline script.
func TestUIHandler_NonCanonicalBasePathNotSpliced(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, basePath := range []string{`/x"><script>alert(1)</script>`, `/x";alert(1);//`, "/bifrost/", "bifrost", "/a/../b"} {
		h := NewUIHandler(uiTestFS(), nil, basePath)
		assert.Nil(t, h.basePathShells, basePath)
		assert.Equal(t, uiTestShell, string(serveUITest(h, "/").Response.Body()), basePath)
	}
}

// TestUIHandler_BasePathShellRewriterSeesPrefixedShell checks the build's
// ShellRewriter still runs, on the base-path-adjusted document.
func TestUIHandler_BasePathShellRewriterSeesPrefixedShell(t *testing.T) {
	var seen string
	rewriter := func(_ *fasthttp.RequestCtx, data []byte) []byte {
		seen = string(data)
		return []byte(strings.ToUpper(string(data)))
	}
	h := NewUIHandler(uiTestFS(), rewriter, "/bifrost")
	body := string(serveUITest(h, "/").Response.Body())
	assert.Contains(t, seen, `window.__BIFROST_BASE_PATH__="/bifrost"`)
	assert.Contains(t, body, `WINDOW.__BIFROST_BASE_PATH__="/BIFROST"`)
}

// TestUIHandler_BasePathShellRewriterCannotMutateCache checks a ShellRewriter
// that edits its input in place does not change the cached base-path shell
// that later requests are served from.
func TestUIHandler_BasePathShellRewriterCannotMutateCache(t *testing.T) {
	var seen []string
	rewriter := func(_ *fasthttp.RequestCtx, data []byte) []byte {
		seen = append(seen, string(data))
		copy(data, "MUTATED")
		return data
	}
	h := NewUIHandler(uiTestFS(), rewriter, "/bifrost")
	serveUITest(h, "/")
	serveUITest(h, "/workspace/logs")
	if assert.Len(t, seen, 2) {
		assert.True(t, strings.HasPrefix(seen[1], "<!doctype html>"), "second request saw a mutated shell: %.20q", seen[1])
	}
}

// TestApplyBasePathToShell_HeadWithAttributes checks the runtime global goes
// right after an opening <head> that carries attributes, not before the
// doctype, and that a <header> element is not mistaken for <head>.
func TestApplyBasePathToShell_HeadWithAttributes(t *testing.T) {
	script := `<script>window.__BIFROST_BASE_PATH__="/b"</script>`
	for _, head := range []string{`<head lang="en">`, "<head\n\tdata-theme=\"dark\">", `<head>`} {
		doc := "<!doctype html><html>" + head + "<title>x</title></head><body><header>h</header></body></html>"
		out := string(applyBasePathToShell([]byte(doc), "/b"))
		assert.True(t, strings.HasPrefix(out, "<!doctype html>"), head)
		assert.Contains(t, out, head+script, head)
	}
	out := string(applyBasePathToShell([]byte(`<html><body><header>h</header></body></html>`), "/b"))
	assert.True(t, strings.HasPrefix(out, script), "no <head>: falls back to document start")
	assert.Contains(t, out, "<header>h</header>")
}

// TestApplyBasePathToShell_QuoteStyles pins that root-absolute refs get the
// prefix whether the attribute is double-quoted, single-quoted or unquoted,
// keeping the original quote, while protocol-relative refs are left alone.
func TestApplyBasePathToShell_QuoteStyles(t *testing.T) {
	doc := `<head></head><script src="/assets/a.js"></script><script src='/assets/b.js'></script><img src=/c.webp><a href='//cdn.example/x'>`
	out := string(applyBasePathToShell([]byte(doc), "/b"))
	assert.Contains(t, out, `src="/b/assets/a.js"`)
	assert.Contains(t, out, `src='/b/assets/b.js'`)
	assert.Contains(t, out, `src=/b/c.webp>`)
	assert.Contains(t, out, `href='//cdn.example/x'`)
}

// BenchmarkUIHandler_ServeShell measures the per-request cost of serving the
// HTML shell: at the domain root, under a base path (one map load), and under a
// base path with a ShellRewriter (which also clones the cached shell), plus a
// static asset for comparison.
func BenchmarkUIHandler_ServeShell(b *testing.B) {
	identity := func(_ *fasthttp.RequestCtx, data []byte) []byte { return data }
	cases := []struct {
		name string
		h    *UIHandler
		path string
	}{
		{"root", NewUIHandler(uiTestFS(), nil, ""), "/workspace/logs"},
		{"prefixed", NewUIHandler(uiTestFS(), nil, "/bifrost"), "/workspace/logs"},
		{"prefixed_rewriter", NewUIHandler(uiTestFS(), identity, "/bifrost"), "/workspace/logs"},
		{"asset", NewUIHandler(uiTestFS(), nil, "/bifrost"), "/assets/index-abc.js"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI(c.path)
			b.ReportAllocs()
			for b.Loop() {
				ctx.Response.Reset()
				c.h.serveDashboard(ctx)
			}
		})
	}
}
