// Adds the compliance logos under the footer logo.
// Mintlify includes any .js file in the content directory on every page, after the page
// becomes interactive. See https://www.mintlify.com/docs/customize/custom-scripts
(function () {
  var badges = [
    { src: "/media/compliance-soc.png", alt: "AICPA SOC" },
    { src: "/media/compliance-gdpr.png", alt: "GDPR" },
    { src: "/media/compliance-iso27001.png", alt: "ISO 27001 certified" },
    { src: "/media/compliance-hipaa.png", alt: "HIPAA" },
  ];

  function mount() {
    var column = document.querySelector("footer.advanced-footer > div > div:first-child > div:first-child");
    if (!column || column.querySelector(".footer-compliance")) return;

    var row = document.createElement("div");
    row.className = "footer-compliance";
    badges.forEach(function (badge) {
      var img = document.createElement("img");
      img.src = badge.src;
      img.alt = badge.alt;
      row.appendChild(img);
    });

    var logo = column.querySelector("a");
    if (logo) logo.insertAdjacentElement("afterend", row);
    else column.prepend(row);
  }

  mount();
  new MutationObserver(mount).observe(document.documentElement, { childList: true, subtree: true });
})();
