// Subscribe to a session's SSE feed and append new blocks to #live.
function juexSubscribe(sessionId, lastEventId) {
  const url = "/api/sessions/" + encodeURIComponent(sessionId) + "/events" +
    (lastEventId ? "?since=" + encodeURIComponent(lastEventId) : "");
  const es = new EventSource(url);
  const live = document.getElementById("live");
  if (!live) return;
  es.addEventListener("message", function (ev) {
    const e = JSON.parse(ev.data);
    const li = document.createElement("li");
    li.textContent = e.type + (e.payload ? " — " + JSON.stringify(e.payload) : "");
    live.appendChild(li);
  });
  es.addEventListener("error", function () {
    es.close();
  });
}

// Wire the prompt form to POST JSON to the session's /turns endpoint.
function juexBindPromptForm(sessionId) {
  const form = document.getElementById("prompt-form");
  if (!form) return;
  form.addEventListener("submit", function (ev) {
    ev.preventDefault();
    const fd = new FormData(form);
    const prompt = fd.get("prompt");
    if (!prompt) return;
    fetch("/api/sessions/" + encodeURIComponent(sessionId) + "/turns", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({prompt: prompt}),
    }).then(function () {
      const ta = form.querySelector("textarea");
      if (ta) ta.value = "";
    });
  });
}
