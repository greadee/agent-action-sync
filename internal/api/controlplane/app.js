"use strict";

const pageSize = 25;
const byId = (id) => document.getElementById(id);
const state = {
  selectedProjectID: "", selectedTaskID: "", selectedAssignmentID: "",
  projects: {items: [], page: {}}, tasks: {items: [], page: {}}, readiness: {items: [], page: {}},
  assignments: {items: [], page: {}}, workers: {items: [], page: {}}, nodes: {items: [], page: {}}
};

function text(id, value) { byId(id).textContent = value === undefined || value === null || value === "" ? "—" : String(value); }
function clear(element) { element.replaceChildren(); }
function show(name) { for (const id of ["loading", "error", "dashboard"]) byId(id).classList.toggle("hidden", id !== name); }
function node(name, className, value) { const element = document.createElement(name); if (className) element.className = className; if (value !== undefined) element.textContent = value; return element; }
function code(value) { return value || "not_reported"; }
function compact(value) { const string = value ? String(value) : ""; return string.length > 22 ? `${string.slice(0, 22)}…` : string || "—"; }

async function responseJSON(response) {
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(value?.error?.message || `Local node returned ${response.status}`);
  return value;
}

async function getJSON(path) { return responseJSON(await fetch(path, {credentials: "same-origin", cache: "no-store", headers: {Accept: "application/json"}})); }
function pagePath(path, cursor) { const url = new URL(path, window.location.origin); url.searchParams.set("limit", String(pageSize)); if (cursor) url.searchParams.set("cursor", cursor); return url.pathname + url.search; }

async function establishSession() {
  const fragment = new URLSearchParams(window.location.hash.slice(1));
  const token = fragment.get("bootstrap");
  if (window.location.hash) history.replaceState(null, "", window.location.pathname + window.location.search);
  if (token) return responseJSON(await fetch("/api/v1/browser-session/bootstrap", {method: "POST", credentials: "same-origin", headers: {"Content-Type": "application/json"}, body: JSON.stringify({bootstrap_token: token})}));
  return getJSON("/api/v1/browser-session");
}

function renderSession(documentValue) {
  const status = documentValue.status || {}; const queue = status.queue || {};
  text("node-state", status.status || "unavailable"); text("lifecycle", status.lifecycle); text("share-count", status.active_share_count);
  text("queue-running", queue.running); text("queue-pending", queue.pending); text("device-id", status.device_id); text("fingerprint", status.fingerprint);
  text("api-version", documentValue.api_version); text("session-expires", new Date(documentValue.session_expires_at).toLocaleString());
  const list = byId("capabilities"); clear(list); for (const capability of documentValue.capabilities || []) list.append(node("li", "", capability));
  byId("health-badge").lastElementChild.textContent = documentValue.health === "ok" ? "Healthy" : "Unavailable";
}

function renderInlineError(message) { const box = byId("visibility-error"); box.textContent = message; box.classList.remove("hidden"); }
function clearInlineError() { const box = byId("visibility-error"); box.textContent = ""; box.classList.add("hidden"); }
function setRefresh(value) { byId("refresh-status").textContent = value; }
function empty(container, title, message) { clear(container); const panel = node("div", "empty-state"); panel.append(node("strong", "", title), node("p", "", message)); container.append(panel); }
function explanation(label, value) { const row = node("div", "explanation-row"); row.append(node("span", "", label), node("code", "", code(value))); return row; }
function statusChip(value) { return node("span", `state-chip state-${String(value || "unknown").replace(/[^a-z0-9_-]/gi, "")}`, value || "unknown"); }

function renderLoadMore(container, collection, label, load) {
  clear(container);
  if (!collection.page?.has_more) return;
  const button = node("button", "load-more", label); button.type = "button";
  button.addEventListener("click", async () => { button.disabled = true; button.textContent = "Loading…"; try { await load(true); } catch (error) { renderInlineError(error instanceof Error ? error.message : "Could not load more results."); } });
  container.append(button);
}

async function loadCollection(key, path, append = false) {
  const collection = state[key]; const value = await getJSON(pagePath(path, append ? collection.page?.next_cursor : ""));
  collection.items = append ? collection.items.concat(value.items || []) : (value.items || []); collection.page = value.page || {}; return value;
}

function currentProject() { return state.projects.items.find((item) => item.project_id === state.selectedProjectID); }
function currentTask() { return state.tasks.items.find((item) => item.task_id === state.selectedTaskID); }

async function loadProjects(append = false) {
  await loadCollection("projects", "/api/v1/orchestration/projects", append);
  if (!state.selectedProjectID && state.projects.items.length) state.selectedProjectID = state.projects.items[0].project_id;
  if (state.selectedProjectID && !currentProject()) state.selectedProjectID = state.projects.items[0]?.project_id || "";
  renderProjects(); renderLoadMore(byId("project-load-more"), state.projects, "Load more projects", loadProjects);
}

function renderProjects() {
  const picker = byId("project-picker"); clear(picker);
  if (!state.projects.items.length) { picker.append(node("option", "", "No local projects available")); picker.disabled = true; empty(byId("project-summary"), "No projects yet", "Register a project through the existing local workflow to view its readiness here."); text("project-count", 0); return; }
  picker.disabled = false;
  for (const project of state.projects.items) { const option = node("option", "", project.display_name || project.project_id); option.value = project.project_id; option.selected = project.project_id === state.selectedProjectID; picker.append(option); }
  const selected = currentProject(); const summary = byId("project-summary"); clear(summary); text("project-count", state.projects.items.length);
  if (selected) { const counts = node("div", "status-counts"); for (const count of [...(selected.assignment_counts || []), ...(selected.gate_counts || [])]) counts.append(node("span", "", `${count.state}: ${count.count}`)); summary.append(node("strong", "", selected.display_name || selected.project_id), node("p", "", `Scheduler ${selected.scheduler_state || "unknown"} · concurrency ${selected.max_concurrent || "—"}`), counts); }
}

async function loadTasks(append = false) {
  if (!state.selectedProjectID) return;
  await loadCollection("tasks", `/api/v1/projects/${encodeURIComponent(state.selectedProjectID)}/tasks`, append);
  if (!state.selectedTaskID && state.tasks.items.length) state.selectedTaskID = state.tasks.items[0].task_id;
  if (state.selectedTaskID && !currentTask()) state.selectedTaskID = state.tasks.items[0]?.task_id || "";
  renderTasks(); renderLoadMore(byId("task-load-more"), state.tasks, "Load more task graphs", loadTasks);
}

function renderTasks() {
  const picker = byId("task-picker"); clear(picker);
  if (!state.selectedProjectID) { picker.disabled = true; picker.append(node("option", "", "Select a project first")); return; }
  if (!state.tasks.items.length) { picker.disabled = true; picker.append(node("option", "", "No task graphs recorded")); empty(byId("task-summary"), "No task graph", "This project has no sanitized task graph projection yet."); text("task-count", 0); return; }
  picker.disabled = false; for (const task of state.tasks.items) { const option = node("option", "", `${task.task_id} · ${task.state}`); option.value = task.task_id; option.selected = task.task_id === state.selectedTaskID; picker.append(option); }
  const task = currentTask(); const summary = byId("task-summary"); clear(summary); text("task-count", state.tasks.items.length);
  if (task) summary.append(statusChip(task.state), explanation("Reason code", task.explanation_code), explanation("Task evidence", task.task_record_id), explanation("Graph evidence", task.graph_record_id));
}

async function loadReadiness(append = false) {
  const task = currentTask(); if (!task || !state.selectedProjectID) { renderReadiness(); return; }
  const url = new URL(`/api/v1/projects/${encodeURIComponent(state.selectedProjectID)}/tasks/${encodeURIComponent(task.task_id)}/readiness`, window.location.origin);
  url.searchParams.set("task_revision", String(task.task_revision)); url.searchParams.set("graph_revision", String(task.graph_revision)); url.searchParams.set("limit", String(pageSize));
  if (append && state.readiness.page?.next_cursor) url.searchParams.set("cursor", state.readiness.page.next_cursor);
  const value = await getJSON(url.pathname + url.search);
  state.readiness.items = append ? state.readiness.items.concat(value.nodes || []) : (value.nodes || []); state.readiness.page = value.page || {}; state.readiness.summary = value;
  renderReadiness(); renderLoadMore(byId("readiness-load-more"), state.readiness, "Load more work packages", loadReadiness);
}

function renderReadiness() {
  const graph = byId("readiness-graph"); clear(graph); const readiness = state.readiness.summary;
  if (!currentTask()) { text("readiness-status", "Waiting for a task"); empty(graph, "No task selected", "Choose a project task graph to inspect package readiness and dependencies."); clear(byId("readiness-meta")); return; }
  if (!readiness) { text("readiness-status", "Loading"); empty(graph, "Loading readiness", "Reading the bounded readiness projection."); return; }
  text("readiness-status", readiness.state || "unknown"); const meta = byId("readiness-meta"); clear(meta); meta.append(explanation("Task reason", readiness.explanation_code), explanation("Event evidence", readiness.event_watermark));
  if (!state.readiness.items.length) { empty(graph, "No work packages", "The selected task graph has no visible work-package nodes."); return; }
  for (const item of state.readiness.items) {
    const card = node("article", "readiness-node"); const header = node("div", "node-heading"); header.append(node("h4", "", item.work_package_id), statusChip(item.state));
    const detail = node("div", "node-details"); detail.append(explanation("Reason code", item.explanation_code), explanation("Evidence ID", item.definition_record_id));
    const dependencies = node("div", "dependencies"); dependencies.append(node("span", "", item.barrier ? "Barrier dependencies" : "Depends on")); const tags = node("ul", "dependency-tags");
    if ((item.dependencies || []).length) for (const dependency of item.dependencies) tags.append(node("li", "", dependency)); else tags.append(node("li", "", "No dependencies")); dependencies.append(tags); card.append(header, detail, dependencies); graph.append(card);
  }
}

async function loadAssignments(append = false) {
  if (!state.selectedProjectID) return;
  await loadCollection("assignments", `/api/v1/projects/${encodeURIComponent(state.selectedProjectID)}/assignments`, append);
  if (!state.selectedAssignmentID && state.assignments.items.length) state.selectedAssignmentID = state.assignments.items[0].assignment_id;
  renderAssignments(); renderLoadMore(byId("assignment-load-more"), state.assignments, "Load more assignments", loadAssignments);
  if (state.selectedAssignmentID) await loadAssignmentDetail(state.selectedAssignmentID);
}

function renderAssignments() {
  const list = byId("assignment-list"); clear(list); text("assignment-count", state.assignments.items.length);
  if (!state.selectedProjectID) { empty(list, "No project selected", "Choose a project to inspect its assignment timeline."); return; }
  if (!state.assignments.items.length) { empty(list, "No assignments", "No supervised work assignments are recorded for this project."); return; }
  for (const assignment of state.assignments.items) {
    const button = node("button", "assignment-row"); button.type = "button"; button.classList.toggle("selected", assignment.assignment_id === state.selectedAssignmentID); button.setAttribute("aria-pressed", String(assignment.assignment_id === state.selectedAssignmentID));
    const top = node("div", "assignment-top"); top.append(node("strong", "", assignment.work_package_id), statusChip(assignment.state)); button.append(top, node("p", "", `Assignment ${assignment.assignment_id} · worker ${assignment.worker_id || "unassigned"} · node ${assignment.node_id || "unassigned"}`));
    if (assignment.failure_code) button.append(explanation("Blocker code", assignment.failure_code));
    button.addEventListener("click", async () => { state.selectedAssignmentID = assignment.assignment_id; renderAssignments(); await loadAssignmentDetail(assignment.assignment_id); }); list.append(button);
  }
}

async function loadAssignmentDetail(assignmentID) {
  const container = byId("assignment-detail"); if (!state.selectedProjectID || !assignmentID) { clear(container); return; }
  empty(container, "Loading assignment detail", "Reading attempts, gates, and closed reason codes.");
  try { renderAssignmentDetail(await getJSON(`/api/v1/projects/${encodeURIComponent(state.selectedProjectID)}/assignments/${encodeURIComponent(assignmentID)}`)); }
  catch (error) { empty(container, "Assignment detail unavailable", error instanceof Error ? error.message : "The assignment could not be read."); }
}

function renderAssignmentDetail(detail) {
  const container = byId("assignment-detail"); clear(container); const header = node("div", "detail-heading"); header.append(node("h4", "", `Assignment ${detail.assignment_id}`), statusChip(detail.state)); container.append(header);
  const facts = node("div", "detail-facts"); facts.append(explanation("Failure / blocker", detail.failure_code), explanation("Execution ID", detail.execution_id), explanation("Worker", detail.worker_id), explanation("Node", detail.node_id)); container.append(facts);
  const timeline = node("section", "timeline"); timeline.append(node("h5", "", "Attempts")); if (!(detail.attempts || []).length) timeline.append(node("p", "muted", "No attempt records are available."));
  for (const attempt of detail.attempts || []) { const item = node("div", "timeline-item"); item.append(node("strong", "", `Attempt ${attempt.attempt_number}`), statusChip(attempt.state), explanation("Recovery", attempt.recovery_disposition), explanation("Failure code", attempt.failure_code), node("time", "", attempt.updated_at || attempt.created_at || "—")); timeline.append(item); }
  const gates = node("section", "gates"); gates.append(node("h5", "", "Gates and decisions")); if (!(detail.gates || []).length) gates.append(node("p", "muted", "No gate records are available."));
  for (const gate of detail.gates || []) { const item = node("div", "gate-item"); item.append(node("strong", "", gate.gate_id), statusChip(gate.status), explanation("Reason code", gate.reason_code), explanation("Evidence digest", compact(gate.digest))); gates.append(item); }
  container.append(timeline, gates);
}

async function loadWorkers(append = false) { await loadCollection("workers", "/api/v1/orchestration/workers", append); renderWorkers(); renderLoadMore(byId("worker-load-more"), state.workers, "Load more workers", loadWorkers); }
async function loadNodes(append = false) { await loadCollection("nodes", "/api/v1/orchestration/nodes", append); renderNodes(); renderLoadMore(byId("node-load-more"), state.nodes, "Load more nodes", loadNodes); }
function renderWorkers() { const list = byId("worker-list"); clear(list); if (!state.workers.items.length) { empty(list, "No workers", "No sanitized worker definitions are registered."); return; } for (const worker of state.workers.items) { const item = node("article", "inventory-item"); item.append(node("strong", "", worker.worker_id), statusChip(worker.lifecycle), node("p", "", `${worker.provider || "provider unknown"} · ${worker.model || "model unknown"}`), explanation("Trade", worker.trade_id), explanation("Runtime", worker.runtime_id)); list.append(item); } }
function renderNodes() { const list = byId("node-list"); clear(list); if (!state.nodes.items.length) { empty(list, "No nodes", "No sanitized node definitions are available."); return; } for (const itemValue of state.nodes.items) { const item = node("article", "inventory-item"); item.append(node("strong", "", itemValue.node_id), statusChip(itemValue.lifecycle), explanation("Version", itemValue.version), explanation("Definition", compact(itemValue.digest))); const capabilities = node("ul", "mini-tags"); for (const capability of itemValue.capability_ids || []) capabilities.append(node("li", "", capability)); item.append(capabilities); list.append(item); } }

function renderVisibilityUnavailable(message) {
  for (const [pickerID, label] of [["project-picker", "Project inventory unavailable"], ["task-picker", "Task inventory unavailable"]]) { const picker = byId(pickerID); clear(picker); picker.append(node("option", "", label)); picker.disabled = true; }
  empty(byId("project-summary"), "Project visibility unavailable", message); empty(byId("task-summary"), "Task visibility unavailable", message);
  empty(byId("readiness-graph"), "Readiness unavailable", message); empty(byId("assignment-list"), "Assignment visibility unavailable", message); clear(byId("assignment-detail"));
  empty(byId("worker-list"), "Worker inventory unavailable", message); empty(byId("node-list"), "Node inventory unavailable", message); text("readiness-status", "Unavailable");
}

async function selectProject(projectID) {
  state.selectedProjectID = projectID; state.selectedTaskID = ""; state.selectedAssignmentID = ""; state.tasks = {items: [], page: {}}; state.readiness = {items: [], page: {}, summary: null}; state.assignments = {items: [], page: {}};
  renderProjects(); renderTasks(); renderReadiness(); renderAssignments(); setRefresh("Loading project visibility…");
  try { await Promise.all([loadTasks(), loadAssignments()]); await loadReadiness(); setRefresh("Sanitized project visibility is current."); } catch (error) { renderInlineError(error instanceof Error ? error.message : "Could not load project visibility."); setRefresh("Project visibility could not be refreshed."); }
}

async function loadVisibility() {
  clearInlineError(); setRefresh("Loading sanitized inventories…");
  try { await loadProjects(); await Promise.all([loadWorkers(), loadNodes()]); if (state.selectedProjectID) await selectProject(state.selectedProjectID); else setRefresh("No local projects are available yet."); }
  catch (error) { const message = error instanceof Error ? error.message : "Could not load local project visibility."; renderInlineError(message); renderVisibilityUnavailable(message); setRefresh("Visibility data is unavailable."); }
}

async function connect() {
  show("loading");
  try { renderSession(await establishSession()); show("dashboard"); await loadVisibility(); }
  catch (error) { text("error-title", "Protected session required"); text("error-message", error instanceof Error ? error.message : "The local node could not establish a browser session."); show("error"); }
}

byId("retry").addEventListener("click", connect);
byId("project-picker").addEventListener("change", (event) => selectProject(event.target.value));
byId("task-picker").addEventListener("change", async (event) => { state.selectedTaskID = event.target.value; state.readiness = {items: [], page: {}, summary: null}; renderTasks(); renderReadiness(); try { await loadReadiness(); } catch (error) { renderInlineError(error instanceof Error ? error.message : "Could not load task readiness."); } });
connect();
