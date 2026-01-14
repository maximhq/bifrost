(function (w, d, s, l, i) {
    w[l] = w[l] || []; w[l].push({
        'gtm.start':
            new Date().getTime(), event: 'gtm.js'
    }); var f = d.getElementsByTagName(s)[0],
        j = d.createElement(s), dl = l != 'dataLayer' ? '&l=' + l : ''; j.async = true; j.src =
            'https://www.googletagmanager.com/gtm.js?id=' + i + dl; f.parentNode.insertBefore(j, f);
})(window, document, 'script', 'dataLayer', 'GTM-PZVSZ6P5');


(function() {
    var script = document.createElement('script');
    script.src = "https://g.getmaxim.ai?id=G-Q9GWB3JQM9";
    script.async = true;
    document.head.appendChild(script);
})();

window.dataLayer = window.dataLayer || [];
function gtag() { dataLayer.push(arguments); }
gtag('js', new Date());
gtag('config', 'G-Q9GWB3JQM9');

// Attach GTM noscript to the top of the body
(function() {
    var noscript = document.createElement('noscript');
    var iframe = document.createElement('iframe');
    iframe.src = "https://www.googletagmanager.com/ns.html?id=GTM-PZVSZ6P5";
    iframe.height = "0";
    iframe.width = "0";
    iframe.style.display = "none";
    iframe.style.visibility = "hidden";
    noscript.appendChild(iframe);

    if (document.body) {
        document.body.insertBefore(noscript, document.body.firstChild);
    } else {
        document.addEventListener('DOMContentLoaded', function() {
            document.body.insertBefore(noscript, document.body.firstChild);
        });
    }
})();