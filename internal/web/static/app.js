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
