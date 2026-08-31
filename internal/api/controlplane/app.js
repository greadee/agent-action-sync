"use strict";

const pageSize = 25;
const byId = (id) => document.getElementById(id);
const state = {
  selectedProjectID: "", selectedTaskID: "", selectedAssignmentID: "",
  projects: {items: [], page: {}}, tasks: {items: [], page: {}}, readiness: {items: [], page: {}},
  assignments: {items: [], page: {}}, workers: {items: [], page: {}}, nodes: {items: [], page: {}}, federatedNodes: [], crossNodeProjects: [],
  csrfToken: "", assignmentDetail: null, dispatchPreview: null, pendingControl: null
};

class LocalNodeError extends Error {
  constructor(status, body) { super(body?.error?.message || `Local node returned ${status}`); this.name = "LocalNodeError"; this.status = status; this.code = body?.error?.code || "request_failed"; this.requestID = body?.error?.request_id || "not_reported"; }
}

function text(id, value) { byId(id).textContent = value === undefined || value === null || value === "" ? "—" : String(value); }
function clear(element) { element.replaceChildren(); }
function show(name) { for (const id of ["loading", "error", "dashboard"]) byId(id).classList.toggle("hidden", id !== name); }
function node(name, className, value) { const element = document.createElement(name); if (className) element.className = className; if (value !== undefined) element.textContent = value; return element; }
function code(value) { return value || "not_reported"; }
function compact(value) { const string = value ? String(value) : ""; return string.length > 22 ? `${string.slice(0, 22)}…` : string || "—"; }

async function responseJSON(response) {
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new LocalNodeError(response.status, value);
  return value;
}

async function getJSON(path) { return responseJSON(await fetch(path, {credentials: "same-origin", cache: "no-store", headers: {Accept: "application/json"}})); }
async function postJSON(path, body) { return responseJSON(await fetch(path, {method: "POST", credentials: "same-origin", cache: "no-store", headers: {Accept: "application/json", "Content-Type": "application/json", "X-SyncGate-CSRF": state.csrfToken}, body: JSON.stringify(body)})); }
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
  state.csrfToken = documentValue.csrf_token || "";
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
function currentAssignment() { return state.assignments.items.find((item) => item.assignment_id === state.selectedAssignmentID); }

async function loadProjects(append = false) {
  await loadCollection("projects", "/api/v1/orchestration/projects", append);
  if (!state.selectedProjectID && state.projects.items.length) state.selectedProjectID = state.projects.items[0].project_id;
  if (state.selectedProjectID && !currentProject()) state.selectedProjectID = state.projects.items[0]?.project_id || "";
  renderProjects(); renderLoadMore(byId("project-load-more"), state.projects, "Load more projects", loadProjects);
}

function renderProjects() {
  const picker = byId("project-picker"); clear(picker);
  if (!state.projects.items.length) { picker.append(node("option", "", "No local projects available")); picker.disabled = true; byId("scheduler-start").disabled = true; byId("scheduler-disable").disabled = true; empty(byId("project-summary"), "No projects yet", "Register a project through the existing local workflow to view its readiness here."); text("project-count", 0); return; }
  picker.disabled = false;
  for (const project of state.projects.items) { const option = node("option", "", project.display_name || project.project_id); option.value = project.project_id; option.selected = project.project_id === state.selectedProjectID; picker.append(option); }
  const selected = currentProject(); const summary = byId("project-summary"); clear(summary); text("project-count", state.projects.items.length);
  if (selected) { const counts = node("div", "status-counts"); for (const count of [...(selected.assignment_counts || []), ...(selected.gate_counts || [])]) counts.append(node("span", "", `${count.state}: ${count.count}`)); summary.append(node("strong", "", selected.display_name || selected.project_id), node("p", "", `Scheduler ${selected.scheduler_state || "unknown"} · concurrency ${selected.max_concurrent || "—"}`), counts); }
  const canControl = Boolean(selected?.selected && selected?.execution_authorized);
  byId("scheduler-start").disabled = !canControl || !selected?.scheduling_enabled || selected?.scheduler_state === "running";
  byId("scheduler-disable").disabled = !canControl || !["running", "draining"].includes(selected?.scheduler_state);
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
  if (!state.selectedProjectID) { picker.disabled = true; byId("task-preview").disabled = true; byId("task-approve").disabled = true; picker.append(node("option", "", "Select a project first")); return; }
  if (!state.tasks.items.length) { picker.disabled = true; byId("task-preview").disabled = true; byId("task-approve").disabled = true; picker.append(node("option", "", "No task graphs recorded")); empty(byId("task-summary"), "No task graph", "This project has no sanitized task graph projection yet."); text("task-count", 0); return; }
  picker.disabled = false; for (const task of state.tasks.items) { const option = node("option", "", `${task.task_id} · ${task.state}`); option.value = task.task_id; option.selected = task.task_id === state.selectedTaskID; picker.append(option); }
  const task = currentTask(); const summary = byId("task-summary"); clear(summary); text("task-count", state.tasks.items.length);
  if (task) summary.append(statusChip(task.state), explanation("Reason code", task.explanation_code), explanation("Task evidence", task.task_record_id), explanation("Graph evidence", task.graph_record_id));
  const canControl = Boolean(task && currentProject()?.selected && currentProject()?.execution_authorized);
  byId("task-preview").disabled = !canControl;
  byId("task-approve").disabled = !canControl || !task?.graph_digest;
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
  const container = byId("assignment-detail"); if (!state.selectedProjectID || !assignmentID) { clear(container); renderEvidenceEmpty("No assignment selected", "Choose an assignment to inspect verifiable decision evidence."); return; }
  empty(container, "Loading assignment detail", "Reading attempts, gates, and closed reason codes.");
  try { state.assignmentDetail = await getJSON(`/api/v1/projects/${encodeURIComponent(state.selectedProjectID)}/assignments/${encodeURIComponent(assignmentID)}`); renderAssignmentDetail(state.assignmentDetail); }
  catch (error) { state.assignmentDetail = null; const message = error instanceof Error ? error.message : "The assignment could not be read."; empty(container, "Assignment detail unavailable", message); renderEvidenceEmpty("Evidence unavailable", message); }
}

function renderEvidenceEmpty(title, message) {
  text("evidence-status", "Unavailable"); empty(byId("assignment-evidence"), title, message); text("incident-status", "Unavailable"); empty(byId("incident-list"), title, message);
}

function renderAssignmentDetail(detail) {
  const container = byId("assignment-detail"); clear(container); const header = node("div", "detail-heading"); header.append(node("h4", "", `Assignment ${detail.assignment_id}`), statusChip(detail.state)); container.append(header);
  const facts = node("div", "detail-facts"); facts.append(explanation("Failure / blocker", detail.failure_code), explanation("Execution ID", detail.execution_id), explanation("Worker", detail.worker_id), explanation("Node", detail.node_id)); container.append(facts);
  const timeline = node("section", "timeline"); timeline.append(node("h5", "", "Attempts")); if (!(detail.attempts || []).length) timeline.append(node("p", "muted", "No attempt records are available."));
  for (const attempt of detail.attempts || []) { const item = node("div", "timeline-item"); item.append(node("strong", "", `Attempt ${attempt.attempt_number}`), statusChip(attempt.state), explanation("Recovery", attempt.recovery_disposition), explanation("Failure code", attempt.failure_code), node("time", "", attempt.updated_at || attempt.created_at || "—")); timeline.append(item); }
  const gates = node("section", "gates"); gates.append(node("h5", "", "Gates and decisions")); if (!(detail.gates || []).length) gates.append(node("p", "muted", "No gate records are available."));
  for (const gate of detail.gates || []) { const item = node("div", "gate-item"); item.append(node("strong", "", gate.gate_id), statusChip(gate.status), explanation("Reason code", gate.reason_code), explanation("Evidence digest", compact(gate.digest))); gates.append(item); }
  const audit = node("section", "audit-timeline"); audit.append(node("h5", "", "Local audit timeline")); if (!(detail.audit || []).length) audit.append(node("p", "muted", "No operator or runtime actions are recorded."));
  for (const event of detail.audit || []) { const item = node("div", "audit-item"); item.append(node("strong", "", event.action), explanation("Reason", event.reason_code), node("span", "muted", `${event.from_state || "initial"} → ${event.to_state}`), node("time", "", event.occurred_at || "—")); audit.append(item); }
  const controls = node("div", "control-actions");
  const actionStates = {pause: ["running", "preparing"], resume: ["paused"], cancel: ["planned", "leased", "preparing", "running", "paused", "collecting", "awaiting_gates"], retry: ["failed", "canceled", "expired"], reassign: ["failed", "canceled", "expired"], evaluate: ["collecting"]};
  for (const [action, label] of Object.entries({pause: "Pause", resume: "Resume", cancel: "Cancel", retry: "Retry", reassign: "Reassign", evaluate: "Evaluate result"})) { const button = node("button", action === "cancel" ? "danger-action" : "", label); button.type = "button"; button.disabled = !actionStates[action].includes(detail.state); button.addEventListener("click", () => openControl(action)); controls.append(button); }
  if (detail.result) { for (const [action, label] of [["approve", "Approve result"], ["reject", "Reject result"]]) { const button = node("button", action === "approve" ? "primary-action" : "danger-action", label); button.type = "button"; button.addEventListener("click", () => openControl(action)); controls.append(button); } }
  container.append(timeline, gates, audit, controls);
  renderDecisionEvidence(detail); renderIncidents(detail);
}

function metric(value, suffix = "") { return value === undefined || value === null ? "not reported" : `${value}${suffix}`; }
function renderDecisionEvidence(detail) {
  const container = byId("assignment-evidence"); clear(container);
  text("evidence-status", detail.result?.ready_for_decision ? "Decision ready" : detail.result ? "Decision evidence" : "Evidence incomplete");
  const budget = detail.observed_budget || {completeness: "unknown", weak_evidence: ["telemetry_not_reported"]};
  const budgetPanel = node("section", "evidence-item budget-evidence"); budgetPanel.append(node("h4", "", "Observed budget"), statusChip(budget.completeness), explanation("Tokens", metric(budget.token_count)), explanation("Cost (micros)", metric(budget.cost_micros)), explanation("Tool calls", metric(budget.tool_calls)));
  const warnings = node("ul", "mini-tags"); for (const warning of budget.weak_evidence || []) warnings.append(node("li", "", warning)); if (warnings.childElementCount) budgetPanel.append(node("p", "muted", "Weak-evidence warnings"), warnings); container.append(budgetPanel);
  const resultPanel = node("section", "evidence-item result-evidence"); resultPanel.append(node("h4", "", "Integration summary"));
  if (!detail.result) resultPanel.append(node("p", "muted", "No evaluated integration summary is available. Approval and rejection remain unavailable until bounded evaluation records one."));
  else {
    resultPanel.append(statusChip(detail.result.ready_for_decision ? "ready" : "pending"), explanation("Result", detail.result.result_id), explanation("Review", detail.result.review_outcome), explanation("Summary digest", compact(detail.result.summary_digest)), explanation("Evidence at", detail.result.evidence_at));
    const tests = node("div", "outcome-list"); tests.append(node("h5", "", "Test outcomes")); if (!(detail.result.tests || []).length) tests.append(node("p", "muted", "No test outcomes were recorded."));
    for (const test of detail.result.tests || []) { const item = node("div", "outcome-item"); item.append(node("strong", "", test.gate_id), statusChip(test.outcome), explanation("Evidence", compact(test.evidence_digest)), explanation("Duration (ms)", metric(test.duration_milliseconds))); tests.append(item); }
    const caveats = node("ul", "mini-tags"); for (const value of [...(detail.result.limitations || []), ...(detail.result.unresolved_issues || [])]) caveats.append(node("li", "", value)); if (caveats.childElementCount) resultPanel.append(node("p", "muted", "Bounded limitations"), caveats); resultPanel.append(tests);
  }
  container.append(resultPanel);
  const telemetryPanel = node("section", "evidence-item telemetry-evidence"); telemetryPanel.append(node("h4", "", "Telemetry completeness"));
  if (!(detail.telemetry || []).length) telemetryPanel.append(node("p", "muted", "No verified telemetry summary is available for this execution."));
  for (const itemValue of detail.telemetry || []) { const item = node("div", "telemetry-item"); item.append(node("strong", "", itemValue.telemetry_id), statusChip(itemValue.completeness), explanation("Outcome", itemValue.final_outcome), explanation("Recorded", itemValue.created_at), explanation("Digest", compact(itemValue.telemetry_digest))); const itemWarnings = node("ul", "mini-tags"); for (const warning of itemValue.weak_evidence || []) itemWarnings.append(node("li", "", warning)); if (itemWarnings.childElementCount) item.append(itemWarnings); telemetryPanel.append(item); }
  container.append(telemetryPanel);
  const acceptedPanel = node("section", "evidence-item accepted-history"); acceptedPanel.append(node("h4", "", "Accepted history"));
  if (!(detail.accepted_history || []).length) acceptedPanel.append(node("p", "muted", "No canonical acceptance is recorded for this assignment."));
  for (const itemValue of detail.accepted_history || []) acceptedPanel.append(node("div", "history-item", `${itemValue.accepted_at} · ${itemValue.reason_code}`), explanation("Summary digest", compact(itemValue.summary_digest)));
  container.append(acceptedPanel);
}

function renderIncidents(detail) {
  const container = byId("incident-list"); clear(container); const incidents = detail.incidents || []; text("incident-status", incidents.length ? `${incidents.length} active` : "No active incidents");
  if (!incidents.length) { empty(container, "No active incident signals", "The current assignment has no stale-lease, uncertain-runtime, leaked-context, unsafe-output, or runaway-process signal."); return; }
  for (const incident of incidents) {
    const item = node("article", `incident-item severity-${incident.severity}`); const heading = node("div", "detail-heading"); heading.append(node("h4", "", incident.kind.replace(/_/g, " ")), statusChip(incident.severity)); item.append(heading, explanation("Status", incident.status), explanation("Evidence code", incident.evidence_code));
    const actions = node("div", "control-actions"); for (const action of incident.recovery_actions || []) { if (action === "await_reconciliation") { item.append(node("p", "muted", "This signal is fenced. Wait for local scheduler reconciliation; the browser cannot override an expired lease.")); continue; } if (!["cancel", "fail", "retry", "reassign"].includes(action)) continue; const button = node("button", action === "cancel" || action === "fail" ? "danger-action" : "", controlLabel(action)); button.type = "button"; button.addEventListener("click", () => openControl(action)); actions.append(button); } if (actions.childElementCount) item.append(actions); container.append(item);
  }
}

function renderDispatchPreview(value) {
  state.dispatchPreview = value; const container = byId("dispatch-preview"); clear(container); text("dispatch-status", value?.already_present ? "Replayed" : "Current");
  if (!(value?.items || []).length) { empty(container, "No dispatch candidates", "The selected graph has no bounded work-package candidates."); return; }
  for (const itemValue of value.items) { const item = node("article", "preview-item"); item.append(node("strong", "", itemValue.work_package_id), statusChip(itemValue.state)); const reasons = node("ul", "mini-tags"); for (const reason of itemValue.reason_codes || []) reasons.append(node("li", "", code(reason))); item.append(reasons); container.append(item); }
}

async function previewDispatch() {
  const task = currentTask(); if (!task || !state.selectedProjectID) return;
  byId("task-preview").disabled = true; text("dispatch-status", "Loading"); clearInlineError();
  const input = {project_id: state.selectedProjectID, task_id: task.task_id, task_revision: task.task_revision, graph_revision: task.graph_revision, idempotency_key: operationKey("preview")};
  try { renderDispatchPreview(await postJSON(`/api/v1/projects/${encodeURIComponent(state.selectedProjectID)}/dispatch/preview`, input)); }
  catch (error) { text("dispatch-status", "Unavailable"); empty(byId("dispatch-preview"), "Preview unavailable", error instanceof Error ? error.message : "Dispatch preview failed."); }
  finally { renderTasks(); }
}

function operationKey(action) { return `ui-${action}-${crypto.randomUUID()}`; }
function controlLabel(action) { return ({start_scheduler: "Start scheduler", disable_scheduler: "Disable scheduler", approve_task: "Approve task graph", pause: "Pause assignment", resume: "Resume assignment", cancel: "Cancel assignment", fail: "Mark assignment failed", retry: "Retry assignment", reassign: "Reassign assignment", evaluate: "Evaluate result", approve: "Approve result", reject: "Reject result"})[action] || action; }

function openControl(action) {
  const project = currentProject(); const task = currentTask(); const detail = state.assignmentDetail;
  if (!project) return;
  state.pendingControl = {action, idempotencyKey: operationKey(action)}; text("control-title", controlLabel(action));
  const summary = byId("control-summary"); clear(summary); summary.append(explanation("Project", project.project_id), explanation("Execution authority", project.execution_authorized ? "authorized" : "not_authorized"), explanation("Scheduler", project.scheduler_state), explanation("Concurrency ceiling", project.max_concurrent));
  if (action === "approve_task") summary.append(explanation("Task", task?.task_id), explanation("Task revision", task?.task_revision), explanation("Graph revision", task?.graph_revision), explanation("Approval digest", compact(task?.graph_digest)), explanation("Dispatch preview", state.dispatchPreview ? `${state.dispatchPreview.items?.length || 0} bounded candidates` : "not_requested"));
  if (action === "start_scheduler" && task) summary.append(explanation("Selected task", task.task_id), explanation("Task / graph revision", `${task.task_revision}/${task.graph_revision}`), explanation("Graph digest", compact(task.graph_digest)), explanation("Dispatch preview", state.dispatchPreview ? `${state.dispatchPreview.items?.length || 0} bounded candidates` : "not_requested"));
  if (!["start_scheduler", "disable_scheduler", "approve_task"].includes(action) && detail) {
    summary.append(explanation("Assignment", detail.assignment_id), explanation("Current state", detail.state), explanation("Worker", detail.worker_id), explanation("Budget evidence", detail.observed_budget?.completeness || "unknown"), explanation("Gate summary", (detail.gates || []).map((gate) => `${gate.gate_id}:${gate.status}`).join(", ") || "none"));
    if (["approve", "reject"].includes(action)) summary.append(explanation("Result digest", compact(detail.result?.summary_digest)), explanation("Review outcome", detail.result?.review_outcome));
  }
  summary.append(explanation("Idempotency key", state.pendingControl.idempotencyKey));
  const workerLabel = byId("control-worker-label"); workerLabel.classList.toggle("hidden", action !== "reassign");
  if (action === "reassign") { const picker = byId("control-worker"); clear(picker); for (const worker of state.workers.items.filter((item) => item.lifecycle === "active" && item.worker_id !== detail?.worker_id)) { const option = node("option", "", worker.worker_id); option.value = worker.worker_id; picker.append(option); } byId("control-submit").disabled = !picker.options.length; } else byId("control-submit").disabled = false;
  const error = byId("control-error"); error.classList.add("hidden"); error.textContent = ""; byId("control-submit").textContent = "Confirm and submit"; byId("control-dialog").showModal();
}

async function submitControl() {
  const pending = state.pendingControl; const project = currentProject(); const task = currentTask(); const detail = state.assignmentDetail; if (!pending || !project) return;
  const button = byId("control-submit"); button.disabled = true; button.textContent = "Submitting…"; const errorBox = byId("control-error"); errorBox.classList.add("hidden");
  try {
    let result;
    if (pending.action === "start_scheduler" || pending.action === "disable_scheduler") {
      const action = pending.action === "start_scheduler" ? "start" : "disable"; result = await postJSON(`/api/v1/projects/${encodeURIComponent(project.project_id)}/scheduler/${action}`, {project_id: project.project_id, idempotency_key: pending.idempotencyKey});
    } else if (pending.action === "approve_task") {
      result = await postJSON(`/api/v1/projects/${encodeURIComponent(project.project_id)}/tasks/${encodeURIComponent(task.task_id)}/approve`, {project_id: project.project_id, task_id: task.task_id, task_revision: task.task_revision, graph_revision: task.graph_revision, approval_digest: task.graph_digest, idempotency_key: pending.idempotencyKey});
    } else if (pending.action === "approve" || pending.action === "reject") {
      const attempt = detail.attempts?.[detail.attempts.length - 1]; result = await postJSON(`/api/v1/projects/${encodeURIComponent(project.project_id)}/assignments/${encodeURIComponent(detail.assignment_id)}/integration`, {project_id: project.project_id, assignment_id: detail.assignment_id, attempt_id: attempt?.attempt_id, decision: pending.action, summary_digest: detail.result?.summary_digest, reason_code: `operator_${pending.action}d`, idempotency_key: pending.idempotencyKey});
    } else {
      const body = {project_id: project.project_id, assignment_id: detail.assignment_id, action: pending.action, idempotency_key: pending.idempotencyKey}; if (pending.action === "reassign") body.worker_id = byId("control-worker").value; result = await postJSON(`/api/v1/projects/${encodeURIComponent(project.project_id)}/assignments/${encodeURIComponent(detail.assignment_id)}/controls`, body);
    }
    byId("control-dialog").close(); state.pendingControl = null; setRefresh(result?.already_present ? "Control replayed safely from the existing idempotency key." : `${controlLabel(pending.action)} recorded.`); await loadVisibility();
  } catch (error) {
    const codeValue = error instanceof LocalNodeError ? error.code : "request_failed"; const requestValue = error instanceof LocalNodeError ? error.requestID : "not_reported"; errorBox.textContent = `${error instanceof Error ? error.message : "Control failed."} · code ${codeValue} · request ${requestValue}`; errorBox.classList.remove("hidden"); button.disabled = false; button.textContent = "Submit same key again";
  }
}

async function loadWorkers(append = false) { await loadCollection("workers", "/api/v1/orchestration/workers", append); renderWorkers(); renderLoadMore(byId("worker-load-more"), state.workers, "Load more workers", loadWorkers); }
async function loadNodes(append = false) { await loadCollection("nodes", "/api/v1/orchestration/nodes", append); renderNodes(); renderLoadMore(byId("node-load-more"), state.nodes, "Load more nodes", loadNodes); }
function renderWorkers() { const list = byId("worker-list"); clear(list); if (!state.workers.items.length) { empty(list, "No workers", "No sanitized worker definitions are registered."); return; } for (const worker of state.workers.items) { const item = node("article", "inventory-item"); item.append(node("strong", "", worker.worker_id), statusChip(worker.lifecycle), node("p", "", `${worker.provider || "provider unknown"} · ${worker.model || "model unknown"}`), explanation("Trade", worker.trade_id), explanation("Runtime", worker.runtime_id)); list.append(item); } }
function renderNodes() { const list = byId("node-list"); clear(list); if (!state.nodes.items.length) { empty(list, "No nodes", "No sanitized node definitions are available."); return; } for (const itemValue of state.nodes.items) { const item = node("article", "inventory-item"); item.append(node("strong", "", itemValue.node_id), statusChip(itemValue.lifecycle), explanation("Version", itemValue.version), explanation("Definition", compact(itemValue.digest))); const capabilities = node("ul", "mini-tags"); for (const capability of itemValue.capability_ids || []) capabilities.append(node("li", "", capability)); item.append(capabilities); list.append(item); } }

async function loadFederatedNodes() { const value = await getJSON("/api/v1/federation/nodes"); state.federatedNodes = value.items || []; renderFederatedNodes(); }
function renderFederatedNodes() {
  const list = byId("federated-node-list"); clear(list); text("federation-count", state.federatedNodes.length);
  if (!state.federatedNodes.length) { empty(list, "No visible paired nodes", "A trusted peer needs an active read-only status grant and a verified status replica before it appears here."); return; }
  for (const itemValue of state.federatedNodes) {
    const item = node("article", "federated-node-item"); const heading = node("div", "detail-heading"); heading.append(node("h3", "", itemValue.display_name || itemValue.device_id), statusChip(itemValue.connectivity)); item.append(heading, explanation("Device", itemValue.device_id), explanation("Health", itemValue.health), explanation("Lifecycle", itemValue.lifecycle), explanation("Revision", itemValue.revision), explanation("Watermark", compact(itemValue.watermark)), explanation("Observed", itemValue.observed_at), explanation("Status expiry", itemValue.expires_at));
    const projects = node("div", "federated-projects"); if (!(itemValue.projects || []).length) projects.append(node("p", "muted", "No sanitized project summaries were replicated."));
    for (const project of itemValue.projects || []) { const projectItem = node("section", "federated-project"); projectItem.append(node("strong", "", project.display_name || project.project_id), statusChip(project.scheduler_state), explanation("Project", project.project_id), explanation("Accepted watermark", compact(project.accepted_history_watermark))); const counts = node("div", "status-counts"); for (const count of [...(project.assignment_counts || []), ...(project.gate_counts || [])]) counts.append(node("span", "", `${count.state}: ${count.count}`)); projectItem.append(counts); projects.append(projectItem); }
    item.append(projects); list.append(item);
  }
}

async function loadCrossNodeProjects() { const value = await getJSON("/api/v1/federation/projects"); state.crossNodeProjects = value.items || []; renderCrossNodeProjects(); }
function readableInsight(value) { return String(value || "not reported").replace(/_/g, " "); }
function renderCrossNodeProjects() {
  const list = byId("cross-node-project-list"); clear(list); text("cross-node-project-count", state.crossNodeProjects.length);
  if (!state.crossNodeProjects.length) { empty(list, "No cross-node project observations", "Register a local project or receive a signed paired-node summary to compare read-only distributed state."); return; }
  for (const project of state.crossNodeProjects) {
    const item = node("article", "cross-node-project-item"); const heading = node("div", "detail-heading"); heading.append(node("h3", "", project.display_name || project.project_id), statusChip(project.authority === "local_control_store" ? "authority" : "not_observed")); item.append(heading, explanation("Project", project.project_id), explanation("Scheduler authority", project.authority === "local_control_store" ? "this node's local control store" : "not observed locally"));
    const insights = node("ul", "mini-tags"); for (const insight of project.insights || []) insights.append(node("li", "", readableInsight(insight))); item.append(insights);
    const observations = node("div", "cross-node-observations"); if (!(project.observations || []).length) observations.append(node("p", "muted", "No paired-node replica has reported this local project."));
    for (const observation of project.observations || []) { const replica = node("section", "cross-node-observation"); const replicaHeading = node("div", "detail-heading"); replicaHeading.append(node("h4", "", observation.display_name || observation.device_id), statusChip(observation.observation_kind)); replica.append(replicaHeading, explanation("Replica device", observation.device_id), explanation("Scheduler state", observation.scheduler_state), explanation("Accepted watermark", compact(observation.accepted_history_watermark)), explanation("Snapshot", `${observation.protocol || "unknown"} · r${observation.revision || "—"}`), explanation("Observed", observation.observed_at), explanation("Expires", observation.expires_at)); if (observation.compatibility_message) replica.append(node("p", "muted", observation.compatibility_message)); observations.append(replica); }
    item.append(observations); list.append(item);
  }
}

function renderVisibilityUnavailable(message) {
  for (const [pickerID, label] of [["project-picker", "Project inventory unavailable"], ["task-picker", "Task inventory unavailable"]]) { const picker = byId(pickerID); clear(picker); picker.append(node("option", "", label)); picker.disabled = true; }
  empty(byId("project-summary"), "Project visibility unavailable", message); empty(byId("task-summary"), "Task visibility unavailable", message);
  empty(byId("readiness-graph"), "Readiness unavailable", message); empty(byId("assignment-list"), "Assignment visibility unavailable", message); clear(byId("assignment-detail"));
  renderEvidenceEmpty("Decision evidence unavailable", message);
  empty(byId("worker-list"), "Worker inventory unavailable", message); empty(byId("node-list"), "Node inventory unavailable", message); text("readiness-status", "Unavailable");
  for (const id of ["scheduler-start", "scheduler-disable", "task-preview", "task-approve"]) byId(id).disabled = true;
  text("dispatch-status", "Unavailable"); empty(byId("dispatch-preview"), "Dispatch controls unavailable", message);
}

async function selectProject(projectID) {
  state.selectedProjectID = projectID; state.selectedTaskID = ""; state.selectedAssignmentID = ""; state.assignmentDetail = null; state.dispatchPreview = null; state.tasks = {items: [], page: {}}; state.readiness = {items: [], page: {}, summary: null}; state.assignments = {items: [], page: {}};
  renderProjects(); renderTasks(); renderReadiness(); renderAssignments(); renderEvidenceEmpty("Loading decision evidence", "Assignment evidence will appear after the bounded assignment projection loads."); setRefresh("Loading project visibility…");
  try { await Promise.all([loadTasks(), loadAssignments()]); await loadReadiness(); setRefresh("Sanitized project visibility is current."); } catch (error) { renderInlineError(error instanceof Error ? error.message : "Could not load project visibility."); setRefresh("Project visibility could not be refreshed."); }
}

async function loadVisibility() {
  clearInlineError(); setRefresh("Loading sanitized inventories…");
  try { await loadProjects(); await Promise.all([loadWorkers(), loadNodes()]); if (state.selectedProjectID) await selectProject(state.selectedProjectID); else setRefresh("No local projects are available yet."); }
  catch (error) { const message = error instanceof Error ? error.message : "Could not load local project visibility."; renderInlineError(message); renderVisibilityUnavailable(message); setRefresh("Visibility data is unavailable."); }
}

async function loadFederationVisibility() {
  try { await loadFederatedNodes(); }
  catch (error) { state.federatedNodes = []; text("federation-count", 0); empty(byId("federated-node-list"), "Paired-node visibility unavailable", error instanceof Error ? error.message : "Could not load paired-node status."); }
  try { await loadCrossNodeProjects(); }
  catch (error) { state.crossNodeProjects = []; text("cross-node-project-count", 0); empty(byId("cross-node-project-list"), "Cross-node project view unavailable", error instanceof Error ? error.message : "Could not compare paired-node project observations."); }
}

async function connect() {
  show("loading");
  try { renderSession(await establishSession()); show("dashboard"); await Promise.all([loadVisibility(), loadFederationVisibility()]); }
  catch (error) { text("error-title", "Protected session required"); text("error-message", error instanceof Error ? error.message : "The local node could not establish a browser session."); show("error"); }
}

byId("retry").addEventListener("click", connect);
byId("project-picker").addEventListener("change", (event) => selectProject(event.target.value));
byId("task-picker").addEventListener("change", async (event) => { state.selectedTaskID = event.target.value; state.dispatchPreview = null; text("dispatch-status", "Not requested"); empty(byId("dispatch-preview"), "No preview yet", "Request a bounded preview for this task graph before approval or scheduler start."); state.readiness = {items: [], page: {}, summary: null}; renderTasks(); renderReadiness(); try { await loadReadiness(); } catch (error) { renderInlineError(error instanceof Error ? error.message : "Could not load task readiness."); } });
byId("task-preview").addEventListener("click", previewDispatch);
byId("task-approve").addEventListener("click", () => openControl("approve_task"));
byId("scheduler-start").addEventListener("click", () => openControl("start_scheduler"));
byId("scheduler-disable").addEventListener("click", () => openControl("disable_scheduler"));
byId("control-cancel").addEventListener("click", () => byId("control-dialog").close());
byId("control-submit").addEventListener("click", submitControl);
byId("control-dialog").addEventListener("close", () => { state.pendingControl = null; });
connect();
