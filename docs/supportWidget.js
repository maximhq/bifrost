(function () {
  var PYLON_APP_ID = "ae3ae6af-96e5-4240-aaa5-4ad62d5c062b";
  var DISCORD_URL = "https://getmax.im/bifrost-discord";
  var STORAGE_KEY = "bifrost_support_email";
  var STYLE_ID = "bifrost-support-widget-styles";
  var TRIGGER_ID = "bifrost-support-trigger";
  var OVERLAY_ID = "bifrost-support-overlay";
  var MODAL_ID = "bifrost-support-modal";
  var ERROR_ID = "bifrost-support-error";

  function escapeKeyHandler(event) {
    if (event.key === "Escape") {
      closeModal();
    }
  }

  function safeGetItem(key) {
    try {
      return window.localStorage.getItem(key);
    } catch (err) {
      return null;
    }
  }

  function safeSetItem(key, value) {
    try {
      window.localStorage.setItem(key, value);
    } catch (err) {
      // ignore storage errors
    }
  }

  function isValidEmail(value) {
    if (!value) {
      return false;
    }
    return /^[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}$/i.test(value);
  }

  function nameFromEmail(email) {
    var handle = email.split("@")[0] || "User";
    var cleaned = handle.replace(/[._-]+/g, " ").trim();
    if (!cleaned) {
      return "User";
    }
    return cleaned
      .split(" ")
      .map(function (part) {
        return part.charAt(0).toUpperCase() + part.slice(1);
      })
      .join(" ");
  }

  function getAppId() {
    return PYLON_APP_ID;
  }

  function isPlaceholderAppId(appId) {
    return !appId || appId === "YOUR_PYLON_APP_ID";
  }

  function loadPylonScript(appId) {
    if (isPlaceholderAppId(appId)) {
      return;
    }
    if (window.__pylonWidgetLoaded) {
      return;
    }
    window.__pylonWidgetLoaded = true;
    (function () {
      var e = window;
      var t = document;
      var n = function () {
        n.e(arguments);
      };
      n.q = [];
      n.e = function (args) {
        n.q.push(args);
      };
      e.Pylon = n;
      var r = function () {
        var s = t.createElement("script");
        s.setAttribute("type", "text/javascript");
        s.setAttribute("async", "true");
        s.setAttribute("src", "https://widget.usepylon.com/widget/" + appId);
        var x = t.getElementsByTagName("script")[0];
        x.parentNode.insertBefore(s, x);
      };
      if (t.readyState === "complete") {
        r();
      } else if (e.addEventListener) {
        e.addEventListener("load", r, false);
      } else if (e.attachEvent) {
        e.attachEvent("onload", r);
      }
    })();
  }

  function setPylonUser(email) {
    var appId = getAppId();
    if (isPlaceholderAppId(appId)) {
      var errorEl = document.getElementById(ERROR_ID);
      if (errorEl) {
        errorEl.textContent = "Support chat is not configured yet. Join Discord instead.";
        errorEl.dataset.visible = "true";
      }
      return false;
    }

    window.pylon = window.pylon || {};
    window.pylon.chat_settings = Object.assign({}, window.pylon.chat_settings || {}, {
      app_id: appId,
      email: email,
      name: nameFromEmail(email),
    });

    loadPylonScript(appId);
    return true;
  }

  function openChatWhenReady() {
    var attempts = 0;
    var maxAttempts = 20;
    var intervalMs = 300;

    function tryOpen() {
      attempts += 1;
      if (typeof window.Pylon === "function") {
        window.Pylon("show");
        return true;
      }
      return false;
    }

    if (tryOpen()) {
      return;
    }

    var interval = window.setInterval(function () {
      if (tryOpen() || attempts >= maxAttempts) {
        window.clearInterval(interval);
      }
    }, intervalMs);
  }

  function injectStyles() {
    if (document.getElementById(STYLE_ID)) {
      return;
    }
    var style = document.createElement("style");
    style.id = STYLE_ID;
    style.textContent =
      "#" +
      TRIGGER_ID +
      "{position:fixed;right:24px;bottom:24px;z-index:9998;display:inline-flex;align-items:center;gap:10px;padding:10px 16px;border-radius:999px;background:#0f172a;color:#fff;font-size:14px;font-weight:600;letter-spacing:0.01em;box-shadow:0 12px 30px rgba(15,23,42,0.25);border:1px solid rgba(255,255,255,0.12);cursor:pointer;transition:transform 0.2s ease,box-shadow 0.2s ease,background 0.2s ease}" +
      "#" +
      TRIGGER_ID +
      ":hover{transform:translateY(-2px);box-shadow:0 16px 34px rgba(15,23,42,0.3);background:#111c33}" +
      "#" +
      OVERLAY_ID +
      "{position:fixed;inset:0;z-index:9999;display:none;align-items:center;justify-content:center;padding:24px;background:rgba(15,23,42,0.45);backdrop-filter:blur(6px)}" +
      "#" +
      OVERLAY_ID +
      "[data-open='true']{display:flex}" +
      "#" +
      MODAL_ID +
      "{width:100%;max-width:420px;background:#fff;color:#0f172a;border-radius:16px;border:1px solid rgba(15,23,42,0.12);box-shadow:0 28px 70px rgba(15,23,42,0.25);padding:24px;font-family:inherit}" +
      "#" +
      MODAL_ID +
      " h2{margin:0 0 8px 0;font-size:20px;font-weight:700}" +
      "#" +
      MODAL_ID +
      " p{margin:0 0 16px 0;color:#475569;font-size:14px;line-height:1.5}" +
      "#" +
      MODAL_ID +
      " label{display:block;font-size:13px;font-weight:600;color:#0f172a;margin-bottom:6px}" +
      "#" +
      MODAL_ID +
      " input{width:100%;padding:10px 12px;border-radius:10px;border:1px solid rgba(15,23,42,0.15);font-size:14px;outline:none;transition:border 0.2s ease,box-shadow 0.2s ease}" +
      "#" +
      MODAL_ID +
      " input:focus{border-color:#0f172a;box-shadow:0 0 0 3px rgba(15,23,42,0.12)}" +
      "#" +
      MODAL_ID +
      " .bifrost-support-actions{display:flex;flex-direction:column;gap:10px;margin-top:16px}" +
      "#" +
      MODAL_ID +
      " .bifrost-support-primary{width:100%;padding:10px 14px;border-radius:10px;border:none;background:#0f172a;color:#fff;font-size:14px;font-weight:600;cursor:pointer;transition:background 0.2s ease}" +
      "#" +
      MODAL_ID +
      " .bifrost-support-primary:hover{background:#111c33}" +
      "#" +
      MODAL_ID +
      " .bifrost-support-secondary{width:100%;padding:10px 14px;border-radius:10px;border:1px solid rgba(15,23,42,0.15);background:#f8fafc;color:#0f172a;font-size:14px;font-weight:600;text-align:center;text-decoration:none;transition:background 0.2s ease,border 0.2s ease}" +
      "#" +
      MODAL_ID +
      " .bifrost-support-secondary:hover{background:#f1f5f9;border-color:rgba(15,23,42,0.25)}" +
      "#" +
      MODAL_ID +
      " .bifrost-support-footnote{margin-top:14px;font-size:12px;color:#94a3b8}" +
      "#" +
      MODAL_ID +
      " .bifrost-support-close{position:absolute;top:16px;right:16px;border:none;background:transparent;color:#94a3b8;font-size:18px;cursor:pointer}" +
      "#" +
      MODAL_ID +
      " .bifrost-support-error{display:none;margin-top:8px;font-size:12px;color:#dc2626}" +
      "#" +
      MODAL_ID +
      " .bifrost-support-error[data-visible='true']{display:block}" +
      "@media (max-width: 640px){#" +
      MODAL_ID +
      "{padding:20px}#" +
      TRIGGER_ID +
      "{right:16px;bottom:16px}}";
    document.head.appendChild(style);
  }

  function buildTrigger() {
    if (document.getElementById(TRIGGER_ID)) {
      return;
    }
    var trigger = document.createElement("button");
    trigger.id = TRIGGER_ID;
    trigger.type = "button";
    trigger.textContent = "Support";
    trigger.addEventListener("click", function () {
      openModal();
    });
    document.body.appendChild(trigger);
  }

  function buildModal() {
    if (document.getElementById(OVERLAY_ID)) {
      return;
    }

    var overlay = document.createElement("div");
    overlay.id = OVERLAY_ID;

    var modal = document.createElement("div");
    modal.id = MODAL_ID;
    modal.setAttribute("role", "dialog");
    modal.setAttribute("aria-modal", "true");
    modal.setAttribute("aria-labelledby", "bifrost-support-title");
    modal.style.position = "relative";

    var closeButton = document.createElement("button");
    closeButton.className = "bifrost-support-close";
    closeButton.type = "button";
    closeButton.setAttribute("aria-label", "Close");
    closeButton.textContent = "x";
    closeButton.addEventListener("click", function () {
      closeModal();
    });

    var title = document.createElement("h2");
    title.id = "bifrost-support-title";
    title.textContent = "Talk to Bifrost support";

    var description = document.createElement("p");
    description.textContent =
      "Enter your email to start a chat with our team. We only use it to identify the conversation.";

    var label = document.createElement("label");
    label.setAttribute("for", "bifrost-support-email");
    label.textContent = "Email address";

    var input = document.createElement("input");
    input.id = "bifrost-support-email";
    input.type = "email";
    input.autocomplete = "email";
    input.placeholder = "you@company.com";

    var error = document.createElement("div");
    error.id = ERROR_ID;
    error.className = "bifrost-support-error";

    var actions = document.createElement("div");
    actions.className = "bifrost-support-actions";

    var primary = document.createElement("button");
    primary.type = "submit";
    primary.className = "bifrost-support-primary";
    primary.textContent = "Start chat";

    var secondary = document.createElement("a");
    secondary.className = "bifrost-support-secondary";
    secondary.href = DISCORD_URL;
    secondary.target = "_blank";
    secondary.rel = "noreferrer";
    secondary.textContent = "Join Discord instead";

    actions.appendChild(primary);
    actions.appendChild(secondary);

    var footnote = document.createElement("div");
    footnote.className = "bifrost-support-footnote";
    footnote.textContent = "Prefer async help? Discord is usually fastest.";

    var form = document.createElement("form");
    form.addEventListener("submit", function (event) {
      event.preventDefault();
      var email = input.value.trim();
      if (!isValidEmail(email)) {
        error.textContent = "Please enter a valid email address.";
        error.dataset.visible = "true";
        return;
      }
      error.dataset.visible = "false";
      var ok = setPylonUser(email);
      if (ok) {
        safeSetItem(STORAGE_KEY, email);
        openChatWhenReady();
        closeModal();
        removeTrigger();
      }
    });

    input.addEventListener("input", function () {
      if (error.dataset.visible === "true") {
        error.dataset.visible = "false";
      }
    });

    form.appendChild(label);
    form.appendChild(input);
    form.appendChild(error);
    form.appendChild(actions);

    modal.appendChild(closeButton);
    modal.appendChild(title);
    modal.appendChild(description);
    modal.appendChild(form);
    modal.appendChild(footnote);

    overlay.addEventListener("click", function (event) {
      if (event.target === overlay) {
        closeModal();
      }
    });

    overlay.appendChild(modal);
    document.body.appendChild(overlay);
  }

  function openModal() {
    var overlay = document.getElementById(OVERLAY_ID);
    if (!overlay) {
      return;
    }
    overlay.dataset.open = "true";
    document.addEventListener("keydown", escapeKeyHandler);
    var input = document.getElementById("bifrost-support-email");
    if (input) {
      input.focus();
    }
  }

  function closeModal() {
    var overlay = document.getElementById(OVERLAY_ID);
    if (!overlay) {
      return;
    }
    overlay.dataset.open = "false";
    document.removeEventListener("keydown", escapeKeyHandler);
  }

  function removeTrigger() {
    var trigger = document.getElementById(TRIGGER_ID);
    if (trigger && trigger.parentNode) {
      trigger.parentNode.removeChild(trigger);
    }
  }

  function boot() {
    injectStyles();

    var appId = getAppId();
    var storedEmail = safeGetItem(STORAGE_KEY);

    if (!isPlaceholderAppId(appId) && isValidEmail(storedEmail)) {
      setPylonUser(storedEmail);
      return;
    }

    buildTrigger();
    buildModal();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
})();
