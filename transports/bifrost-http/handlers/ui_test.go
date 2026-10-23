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

func uiTestFS() fstest.MapFS {
	return fstest.MapFS{
		"ui/index.html":          {Data: []byte(uiTestShell)},
		"ui/assets/index-abc.js": {Data: []byte("console.log(1)")},
		"ui/favicon.ico":         {Data: []byte("ico")},
	}
}

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
