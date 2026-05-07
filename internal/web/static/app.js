// juex web client. Single entry point: juexInitSession(sessionId).

function juexInitSession(sessionId) {
  const state = { sessionId, status: "idle" };
  bindPromptForm(state);
  bindInterruptButton(state);
  refreshMessages(state).then(function () {
    subscribeEvents(state);
  });
}

function setStatus(state, label, kind) {
  state.status = kind;
  const el = document.getElementById("status");
  if (!el) return;
  el.className = "status status-" + kind;
  el.innerHTML = '<span class="status-dot"></span>' + escapeText(label);
}

function bindPromptForm(state) {
  const form = document.getElementById("prompt-form");
  if (!form) return;
  form.addEventListener("submit", function (ev) {
    ev.preventDefault();
    const ta = form.querySelector("textarea");
    const prompt = ta && ta.value.trim();
    if (!prompt) return;
    fetch("/api/sessions/" + encodeURIComponent(state.sessionId) + "/turns", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt: prompt }),
    }).then(function (r) {
      if (r.status === 409) {
        setStatus(state, "turn already running", "error");
        return;
      }
      if (ta) ta.value = "";
    });
  });
}

function bindInterruptButton(state) {
  const btn = document.getElementById("interrupt-btn");
  if (!btn) return;
  btn.addEventListener("click", function () {
    fetch("/api/sessions/" + encodeURIComponent(state.sessionId) + "/interrupt", { method: "POST" });
  });
}

function refreshMessages(state) {
  return fetch("/api/sessions/" + encodeURIComponent(state.sessionId))
    .then(function (r) { return r.json(); })
    .then(function (data) {
      renderMessages(data.messages || []);
      const turns = document.getElementById("turns-count");
      if (turns) turns.textContent = data.turns;
    })
    .catch(function () { /* swallow; SSE will refresh again */ });
}

function subscribeEvents(state) {
  const url = "/api/sessions/" + encodeURIComponent(state.sessionId) + "/events";
  const es = new EventSource(url);
  es.addEventListener("message", function (ev) {
    let e;
    try { e = JSON.parse(ev.data); } catch (_) { return; }
    handleEvent(state, e);
  });
  es.addEventListener("error", function () {
    es.close();
    // Reconnect after a delay so transient blips recover.
    setTimeout(function () { subscribeEvents(state); }, 2000);
  });
}

function handleEvent(state, e) {
  switch (e.type) {
    case "turn.started":
      setStatus(state, "running…", "running");
      break;
    case "tool.requested":
      setStatus(state, "tool: " + ((e.payload && e.payload.tool_name) || "?"), "tool");
      break;
    case "tool.completed":
    case "tool.errored":
      setStatus(state, "running…", "running");
      break;
    case "turn.completed":
      refreshMessages(state).then(function () {
        setStatus(state, "done", "done");
        setTimeout(function () { setStatus(state, "idle", "idle"); }, 1500);
      });
      break;
    case "turn.errored":
      refreshMessages(state).then(function () {
        setStatus(state, "error", "error");
      });
      break;
  }
}

function renderMessages(messages) {
  const root = document.getElementById("messages");
  if (!root) return;
  root.innerHTML = "";
  for (const m of messages) {
    root.appendChild(renderMessage(m));
  }
}

function renderMessage(m) {
  const el = document.createElement("article");
  el.className = "msg msg-" + (m.role || "unknown");
  const header = document.createElement("header");
  header.className = "msg-role";
  header.textContent = m.role || "?";
  el.appendChild(header);
  const body = document.createElement("div");
  body.className = "msg-body";
  for (const b of (m.blocks || [])) {
    body.appendChild(renderBlock(b));
  }
  el.appendChild(body);
  return el;
}

function renderBlock(b) {
  switch (b.type) {
    case "text":
      return el("div", "block-text", b.text || "");
    case "reasoning":
      return collapsed("Thinking", b.text || (b.redacted ? "[redacted]" : ""), "block-thinking");
    case "tool_use": {
      const root = document.createElement("div");
      root.className = "block-tool-use";
      const head = document.createElement("div");
      head.className = "tool-head";
      head.appendChild(el("span", "tool-name", b.tool_name || "?"));
      const id = el("span", "tool-id", b.tool_use_id ? "#" + b.tool_use_id : "");
      head.appendChild(id);
      root.appendChild(head);
      root.appendChild(el("pre", "tool-input", prettyJSON(b.input)));
      return root;
    }
    case "tool_result": {
      const text = b.content || "";
      const summary = (b.is_error ? "[error] " : "") + truncatePreview(text, 120);
      return collapsed(summary, text, "block-tool-result" + (b.is_error ? " is-error" : ""));
    }
    default:
      return el("div", "block-unknown", JSON.stringify(b));
  }
}

function collapsed(summaryText, body, cls) {
  const d = document.createElement("details");
  d.className = cls;
  const s = document.createElement("summary");
  s.textContent = summaryText;
  d.appendChild(s);
  const pre = document.createElement("pre");
  pre.textContent = body;
  d.appendChild(pre);
  return d;
}

function el(tag, cls, text) {
  const x = document.createElement(tag);
  if (cls) x.className = cls;
  if (text !== undefined) x.textContent = text;
  return x;
}

function prettyJSON(v) {
  try { return JSON.stringify(v, null, 2); } catch (_) { return String(v); }
}

function truncatePreview(s, n) {
  if (!s) return "(empty)";
  s = s.replace(/\s+/g, " ").trim();
  if (s.length <= n) return s;
  return s.slice(0, n) + "…";
}

function escapeText(s) {
  return String(s);
}
