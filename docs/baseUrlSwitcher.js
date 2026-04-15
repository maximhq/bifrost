/**
 * Bifrost docs — Base URL persistence
 *
 * The OpenAPI spec exposes the gateway base URL as a server variable
 * (`{baseUrl}`), so Mintlify's API Reference playground renders an
 * editable input for it. This script:
 *
 *   1. Preloads that input from localStorage on every page load /
 *      SPA route change, so the user only has to type their URL once.
 *   2. Persists any edit the user makes back to localStorage.
 *   3. Rewrites every `<code>` block in the MDX docs that mentions the
 *      default `http://localhost:8080`, so curl/SDK examples on the
 *      regular doc pages also use the configured URL.
 *
 * Mintlify auto-injects any `.js` file in the docs root on every page,
 * so no docs.json wiring is required.
 */
(function () {
  if (typeof window === "undefined" || typeof document === "undefined") return;
  if (window.__bifrostBaseUrlSwitcherLoaded) return;
  window.__bifrostBaseUrlSwitcherLoaded = true;

  var DEFAULT_URL = "http://localhost:8080";
  var STORAGE_KEY = "bifrost_base_url";
  // Per-element snapshot of original text-node values, keyed via a
  // WeakMap so detached DOM nodes get GC'd cleanly.
  var snapshots = new WeakMap();

  function readStoredUrl() {
    try {
      var v = window.localStorage.getItem(STORAGE_KEY);
      return v && v.trim() ? v.trim() : DEFAULT_URL;
    } catch (e) {
      return DEFAULT_URL;
    }
  }

  function writeStoredUrl(url) {
    try {
      window.localStorage.setItem(STORAGE_KEY, url);
    } catch (e) {
      /* ignore quota / private mode */
    }
  }

  function normalizeUrl(input) {
    if (!input) return DEFAULT_URL;
    var url = String(input).trim();
    if (!url) return DEFAULT_URL;
    if (!/^https?:\/\//i.test(url)) url = "http://" + url;
    return url.replace(/\/+$/, "");
  }

  /**
   * Snapshot every text node inside `el` and remember the original
   * value, so subsequent URL changes can always rewrite from the
   * canonical source. Returns the snapshot, or null if the block has
   * no localhost reference (so we never visit it again).
   */
  function snapshotTextNodes(el) {
    var walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT, null);
    var entries = [];
    var hasMatch = false;
    var node;
    while ((node = walker.nextNode())) {
      var text = node.nodeValue || "";
      if (text.indexOf("localhost:8080") !== -1) hasMatch = true;
      entries.push({ node: node, original: text });
    }
    return hasMatch ? entries : null;
  }

  /**
   * Rewrite every `<code>` block that mentions the default localhost
   * URL. We only touch text nodes (`nodeValue`) — never `innerHTML` —
   * so there is no path where a string is reinterpreted as HTML.
   */
  function rewriteCodeBlocks(currentUrl) {
    var blocks = document.querySelectorAll("pre code, code");
    var bareUrl = currentUrl.replace(/^https?:\/\//, "");
    for (var i = 0; i < blocks.length; i++) {
      var el = blocks[i];
      var entries = snapshots.get(el);
      if (entries === undefined) {
        entries = snapshotTextNodes(el);
        // Cache the result either way (null = "scanned, no match")
        // so we don't re-walk this element on every observer tick.
        snapshots.set(el, entries);
      }
      if (!entries) continue;
      for (var j = 0; j < entries.length; j++) {
        var entry = entries[j];
        var next;
        if (currentUrl === DEFAULT_URL) {
          next = entry.original;
        } else {
          next = entry.original
            .replace(/https?:\/\/localhost:8080/g, currentUrl)
            .replace(/localhost:8080/g, bareUrl);
        }
        if (entry.node.nodeValue !== next) entry.node.nodeValue = next;
      }
    }
  }

  // ---------- API Reference playground sync ----------

  /**
   * Mintlify renders the server-variable field with a stable id of
   * `api-playground-input`, so we can scope directly to it instead of
   * heuristically scanning every text input on the page.
   */
  function findPlaygroundUrlInputs() {
    var el = document.getElementById("api-playground-input");
    if (!el || el.__bifrostPlaygroundBound) return [];
    return [el];
  }

  function setNativeValue(el, value) {
    // React overrides the input's value setter; bypass it so React's
    // controlled state picks up the programmatic change.
    var proto = Object.getPrototypeOf(el);
    var descriptor = Object.getOwnPropertyDescriptor(proto, "value");
    if (descriptor && descriptor.set) descriptor.set.call(el, value);
    else el.value = value;
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
  }

  function syncPlaygroundInputs(state) {
    var inputs = findPlaygroundUrlInputs();
    for (var i = 0; i < inputs.length; i++) {
      var el = inputs[i];
      if (el.__bifrostPlaygroundBound) continue;
      el.__bifrostPlaygroundBound = true;

      // Persist on blur / change. Using `change` (not `input`) avoids
      // fighting the user mid-keystroke.
      el.addEventListener("change", function (e) {
        var v = normalizeUrl(e.target.value);
        state.currentUrl = v;
        writeStoredUrl(v);
        rewriteCodeBlocks(v);
      });

      // Preload from storage exactly once. After this, the input is
      // user-owned — we never write to it again, otherwise typing would
      // get clobbered by the next MutationObserver tick.
      if (state.currentUrl !== DEFAULT_URL && el.value !== state.currentUrl) {
        setNativeValue(el, state.currentUrl);
      }
    }
  }

  // ---------- Boot ----------

  function boot() {
    var state = { currentUrl: normalizeUrl(readStoredUrl()) };
    rewriteCodeBlocks(state.currentUrl);
    syncPlaygroundInputs(state);

    // Mintlify is an SPA — re-run on any DOM mutation (debounced).
    var pending = false;
    var observer = new MutationObserver(function () {
      if (pending) return;
      pending = true;
      window.requestAnimationFrame(function () {
        pending = false;
        rewriteCodeBlocks(state.currentUrl);
        syncPlaygroundInputs(state);
      });
    });
    observer.observe(document.body, { childList: true, subtree: true });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
})();
