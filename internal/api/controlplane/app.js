"use strict";

const byId = (id) => document.getElementById(id);

function text(id, value) {
  byId(id).textContent = value === undefined || value === null || value === "" ? "—" : String(value);
}

function show(name) {
  for (const id of ["loading", "error", "dashboard"]) byId(id).classList.toggle("hidden", id !== name);
}

async function responseJSON(response) {
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(value?.error?.message || `Local node returned ${response.status}`);
  return value;
}

async function establishSession() {
  const fragment = new URLSearchParams(window.location.hash.slice(1));
  const token = fragment.get("bootstrap");
  if (window.location.hash) history.replaceState(null, "", window.location.pathname + window.location.search);
  if (token) {
    return responseJSON(await fetch("/api/v1/browser-session/bootstrap", {
      method: "POST",
      credentials: "same-origin",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({bootstrap_token: token})
    }));
  }
  return responseJSON(await fetch("/api/v1/browser-session", {credentials: "same-origin", cache: "no-store"}));
}

function render(documentValue) {
  const status = documentValue.status || {};
  const queue = status.queue || {};
  text("node-state", status.status || "unavailable");
  text("lifecycle", status.lifecycle);
  text("share-count", status.active_share_count);
  text("queue-running", queue.running);
  text("queue-pending", queue.pending);
  text("device-id", status.device_id);
  text("fingerprint", status.fingerprint);
  text("api-version", documentValue.api_version);
  text("session-expires", new Date(documentValue.session_expires_at).toLocaleString());
  const list = byId("capabilities");
  list.replaceChildren(...(documentValue.capabilities || []).map((capability) => {
    const item = document.createElement("li");
    item.textContent = capability;
    return item;
  }));
  const badge = byId("health-badge");
  badge.lastElementChild.textContent = documentValue.health === "ok" ? "Healthy" : "Unavailable";
  show("dashboard");
}

async function connect() {
  show("loading");
  try {
    render(await establishSession());
  } catch (error) {
    text("error-title", "Protected session required");
    text("error-message", error instanceof Error ? error.message : "The local node could not establish a browser session.");
    show("error");
  }
}

byId("retry").addEventListener("click", connect);
connect();
