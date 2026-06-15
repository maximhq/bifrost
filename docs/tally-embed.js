// Loads the Tally embed runtime so `data-tally-open` popup buttons work across the docs.
// Mintlify includes any .js file in the content directory on every page, after the page
// becomes interactive. See https://www.mintlify.com/docs/customize/custom-scripts
(function () {
  var widgetScriptSrc = "https://tally.so/widgets/embed.js";

  var load = function () {
    if (typeof Tally !== "undefined") {
      Tally.loadEmbeds();
    }
  };

  if (typeof Tally !== "undefined") {
    load();
  } else if (document.querySelector('script[src="' + widgetScriptSrc + '"]') == null) {
    var script = document.createElement("script");
    script.src = widgetScriptSrc;
    script.onload = load;
    script.onerror = load;
    document.body.appendChild(script);
  }
})();
