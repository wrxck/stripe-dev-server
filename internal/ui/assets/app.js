const state = { tab: "captures", captures: [], selected: null };

const els = {
  tabs: document.querySelectorAll(".tab"),
  views: document.querySelectorAll(".view"),
  list: document.getElementById("captures-list"),
  detail: document.getElementById("captures-detail"),
  refresh: document.getElementById("refresh"),
  clear: document.getElementById("clear"),
  webhookForm: document.getElementById("webhook-form"),
  webhookResult: document.getElementById("webhook-result"),
};

async function loadCaptures() {
  const res = await fetch("/_dev/captures");
  state.captures = await res.json() || [];
  renderCaptures();
}

async function clearCaptures() {
  if (state.tab !== "captures") return;
  await fetch("/_dev/captures", { method: "DELETE" });
  state.selected = null;
  await loadCaptures();
}

function statusClass(s) {
  if (s >= 200 && s < 300) return "s2";
  if (s >= 400 && s < 500) return "s4";
  if (s >= 500) return "s5";
  return "";
}

function renderCaptures() {
  if (!state.captures.length) {
    els.list.innerHTML = `<div class="empty">No captured Stripe API calls yet.<br>Point your app at this server and try a request.</div>`;
    els.detail.innerHTML = `<p class="muted">Select a capture to inspect.</p>`;
    return;
  }
  els.list.innerHTML = "";
  for (const c of state.captures) {
    const row = document.createElement("div");
    row.className = "row";
    if (state.selected && state.selected.id === c.id) row.classList.add("selected");
    row.innerHTML = `
      <div class="top">
        <span><span class="method">${escape(c.method)}</span> <span class="ts">${formatTime(c.timestamp)}</span></span>
        <span class="status ${statusClass(c.status)}">${c.status}</span>
      </div>
      <div class="path">${escape(c.path)}${c.query ? "?" + escape(c.query) : ""}</div>
    `;
    row.onclick = () => { state.selected = c; renderCaptures(); renderDetail(); };
    els.list.appendChild(row);
  }
  if (!state.selected) {
    state.selected = state.captures[0];
    els.list.firstChild.classList.add("selected");
  }
  renderDetail();
}

function renderDetail() {
  const c = state.selected;
  if (!c) { els.detail.innerHTML = `<p class="muted">Select a capture to inspect.</p>`; return; }
  els.detail.innerHTML = `
    <h2>${escape(c.method)} ${escape(c.path)}${c.query ? "?" + escape(c.query) : ""}</h2>
    <div class="meta">${c.status} · ${c.durationMs}ms · ${formatTime(c.timestamp)}</div>
    <h3>Request body</h3>
    <pre>${escape(c.requestBody || "(empty)")}</pre>
    <h3>Response body</h3>
    <pre>${escape(prettyJSON(c.responseBody))}</pre>
    <h3>Request headers</h3>
    <pre>${escape(formatHeaders(c.requestHeaders))}</pre>
    <h3>Response headers</h3>
    <pre>${escape(formatHeaders(c.responseHeaders))}</pre>
  `;
}

function formatHeaders(h) {
  if (!h) return "";
  return Object.entries(h).map(([k, v]) => `${k}: ${v}`).join("\n");
}

function prettyJSON(s) {
  if (!s) return "";
  try { return JSON.stringify(JSON.parse(s), null, 2); }
  catch { return String(s); }
}

function formatTime(ts) {
  if (!ts) return "";
  return new Date(ts).toLocaleTimeString();
}

function escape(s) {
  return String(s ?? "")
    .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}

els.tabs.forEach(tab => tab.addEventListener("click", () => {
  els.tabs.forEach(t => t.classList.remove("active"));
  tab.classList.add("active");
  state.tab = tab.dataset.tab;
  document.getElementById("captures-view").classList.toggle("active", state.tab === "captures");
  document.getElementById("webhooks-view").classList.toggle("active", state.tab === "webhooks");
}));

els.refresh.addEventListener("click", loadCaptures);
els.clear.addEventListener("click", clearCaptures);

els.webhookForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const form = new FormData(els.webhookForm);
  const eventType = form.get("eventType");
  const targetUrl = form.get("targetUrl");
  let dataObject;
  try { dataObject = JSON.parse(form.get("dataObject")); }
  catch (err) { els.webhookResult.textContent = "Invalid JSON: " + err.message; return; }
  els.webhookResult.textContent = "Sending...";
  try {
    const res = await fetch("/_dev/webhooks/trigger", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ eventType, targetUrl, dataObject }),
    });
    const out = await res.json();
    els.webhookResult.textContent = JSON.stringify(out, null, 2);
  } catch (err) {
    els.webhookResult.textContent = "Failed: " + err.message;
  }
});

loadCaptures();
setInterval(() => { if (state.tab === "captures") loadCaptures(); }, 3000);
