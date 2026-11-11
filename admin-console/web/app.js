const byId = (id) => document.getElementById(id);
const views = ["login", "recover", "update-password"];
let currentUser = null;
let resetUserID = "";
let resetSecret = "";
let overviewRefreshing = false;

function notice(message, kind = "info") {
  const node = byId("notice");
  node.textContent = message;
  node.className = "notice " + kind;
  node.classList.remove("hidden");
}
function clearNotice() {
  byId("notice").textContent = "";
  byId("notice").className = "notice hidden";
}
function showView(name) {
  clearNotice();
  for (const view of views) {
    const form = byId(view === "login" ? "sign-in-form" : view === "signup" ? "sign-up-form" : view === "recover" ? "recover-form" : "update-password-form");
    form.classList.toggle("hidden", view !== name);
  }
  const copy = {
    login: ["Sign in", "Use your approved Testagram staff account."],

    recover: ["Reset password", "We'll email you a secure recovery link if the account exists."],
    "update-password": ["Choose a new password", "Set a new password using your recovery link."]
  }[name];
  byId("auth-title").textContent = copy[0];
  byId("auth-subtitle").textContent = copy[1];
}
async function api(path, body, method) {
  const response = await fetch(path, {
    method: method || (body === undefined ? "GET" : "POST"),
    credentials: "same-origin",
    headers: body === undefined ? { "Accept": "application/json" } : { "Content-Type": "application/json", "Accept": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store"
  });
  let data = {};
  try { data = await response.json(); } catch (_) {}
  if (!response.ok) throw new Error(data.error || data.message || "Request could not be completed.");
  return data;
}
function busy(form, value) {
  const button = form.querySelector('button[type="submit"]');
  if (!button) return;
  button.disabled = value;
  button.dataset.originalText ||= button.textContent;
  button.textContent = value ? "Please wait…" : button.dataset.originalText;
}
function hideScreens() {
  byId("auth-screen").classList.add("hidden");
  byId("pending-screen").classList.add("hidden");
  byId("dashboard-screen").classList.add("hidden");
  byId("sign-out").classList.add("hidden");
}
function renderPending(email) {
  hideScreens();
  byId("pending-screen").classList.remove("hidden");
  byId("sign-out").classList.remove("hidden");
  byId("pending-screen").querySelector("p").textContent = email
    ? "Signed in as " + email + ", but your account has no staff role yet. The Testagram owner must invite you or assign an approved role before you can enter the operations workspace."
    : "Your account has no staff role yet. The Testagram owner must approve your access before you can enter the operations workspace.";
}
function renderDashboard(user) {
  hideScreens();
  currentUser = user;
  byId("dashboard-screen").classList.remove("hidden");
  byId("sign-out").classList.remove("hidden");
  byId("identity-email").textContent = user.email || "Staff member";
  byId("identity-role").textContent = (user.role || "staff").replace(/\b\w/g, (c) => c.toUpperCase());
  byId("avatar").textContent = (user.email || "T").trim().slice(0, 1).toUpperCase();
  byId("welcome").textContent = "Signed in as " + (user.email || "an approved staff member") + ". Service metrics refresh on demand.";
  byId("invite-panel").classList.toggle("hidden", user.role !== "owner");
  byId("permission-panel").classList.toggle("hidden", user.role !== "owner");
  byId("staff-directory-panel").classList.toggle("hidden", user.role !== "owner");
  byId("approval-panel").classList.remove("hidden");
  byId("sensitive-request-panel").classList.remove("hidden");
  byId("audit-panel").classList.remove("hidden");
  refreshOverview();
  loadApprovalQueue();
  loadAuditHistory();
  if (user.role === "owner") loadStaffDirectory();
}
async function loadSession() {
  try {
    const state = await api("/api/session");
    if (!state.authenticated) {
      hideScreens();
      byId("auth-screen").classList.remove("hidden");
      return;
    }
    if (!state.staff) { renderPending(state.email); return; }
    renderDashboard(state);
  } catch (_) {
    hideScreens();
    byId("auth-screen").classList.remove("hidden");
    notice("The staff service could not be reached. Check the deployment health and try again.", "error");
  }
}
function setMetric(id, value, available = true) {
  byId(id).textContent = available ? value : "N/A";
}
function metricValue(metrics, key) {
  const item = metrics && metrics[key];
  return item && item.available ? Number(item.value) : null;
}
async function refreshOverview() {
  if (overviewRefreshing) return;
  overviewRefreshing = true;
  byId("refresh").disabled = true;
  byId("api-status-title").textContent = "Checking service health…";
  try {
    const data = await api("/api/overview");
    const m = data.metrics || {};
    const apiHealthy = data.api_health === "healthy";
    byId("api-status-title").textContent = apiHealthy ? "API is responding" : data.api_health === "unhealthy" ? "API readiness check failed" : "API health could not be confirmed";
    byId("api-status-dot").className = "status-indicator " + (apiHealthy ? "good" : "bad");
    byId("last-checked").textContent = "Last checked " + new Date(data.checked_at).toLocaleString();
    const apiUp = metricValue(m, "api_up");
    const platformUp = metricValue(m, "platform_up");
    setMetric("metric-api", apiHealthy ? "Healthy" : data.api_health === "unhealthy" ? "Unhealthy" : "Unknown", data.api_health !== "unknown");
    setMetric("metric-api-foot", apiUp === null ? "Prometheus target unavailable" : apiUp === 1 ? "Prometheus scrape is up" : "Prometheus scrape is down");
    setMetric("metric-platform", platformUp === null ? "N/A" : platformUp === 1 ? "Up" : "Down", platformUp !== null);
    const requests = metricValue(m, "request_rate");
    setMetric("metric-requests", requests === null ? "N/A" : requests.toFixed(2), requests !== null);
    const errors = metricValue(m, "error_rate");
    setMetric("metric-errors", errors === null ? "N/A" : errors.toFixed(3), errors !== null);
    const p95 = metricValue(m, "p95_seconds");
    setMetric("metric-latency", p95 === null ? "N/A" : p95 < 1 ? Math.round(p95 * 1000) + " ms" : p95.toFixed(2) + " s", p95 !== null);
    const acquired = metricValue(m, "db_acquired");
    const max = metricValue(m, "db_max");
    setMetric("metric-db", acquired === null || max === null ? "N/A" : Math.round(acquired) + " / " + Math.round(max), acquired !== null && max !== null);
    const pending = metricValue(m, "outbox_pending");
    setMetric("metric-queue", pending === null ? "N/A" : Math.max(0, Math.round(pending)).toLocaleString(), pending !== null);
    if (data.grafana_url) byId("grafana-link").href = data.grafana_url;
  } catch (error) {
    byId("api-status-title").textContent = "Metrics unavailable";
    byId("api-status-dot").className = "status-indicator bad";
    byId("last-checked").textContent = error.message;
  } finally {
    overviewRefreshing = false;
    byId("refresh").disabled = false;
  }
}

for (const button of document.querySelectorAll("[data-view]")) {
  button.addEventListener("click", () => showView(button.dataset.view));
}

byId("sign-in-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget; busy(form, true);
  try {
    await api("/api/auth/sign-in", { email: byId("login-email").value, password: byId("login-password").value });
    await loadSession();
  } catch (error) { notice(error.message, "error"); }
  finally { busy(form, false); }
});
byId("recover-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget; busy(form, true);
  try {
    const result = await api("/api/auth/recover", { email: byId("recover-email").value });
    showView("login"); notice(result.message || "If the account exists, a password-reset email will be sent.", "success");
  } catch (error) { notice(error.message, "error"); }
  finally { busy(form, false); }
});
byId("update-password-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget; busy(form, true);
  try {
    const result = await api("/api/auth/update-password", { user_id: resetUserID, secret: resetSecret, password: byId("new-password").value });
    history.replaceState(null, "", "/");
    resetUserID = "";
    resetSecret = "";
    showView("login"); notice(result.message || "Password updated. You can now sign in.", "success");
  } catch (error) { notice(error.message, "error"); }
  finally { busy(form, false); }
});
async function signOut() {
  try { await api("/api/auth/sign-out", {}); } catch (_) {}
  currentUser = null;
  hideScreens();
  byId("auth-screen").classList.remove("hidden");
  showView("login");
}
byId("sign-out").addEventListener("click", signOut);
byId("pending-sign-out").addEventListener("click", signOut);
byId("refresh").addEventListener("click", refreshOverview);
// Refresh live operational signals every 30 seconds only while an approved staff member
// is viewing the dashboard. Pause polling in hidden tabs to avoid needless load.
window.setInterval(() => {
  const dashboard = byId("dashboard-screen");
  if (currentUser && dashboard && !dashboard.classList.contains("hidden") && document.visibilityState === "visible") {
    refreshOverview();
  }
}, 30000);
byId("invite-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget; busy(form, true);
  try {
    const result = await api("/api/staff/invite", { email: byId("invite-email").value, role: byId("invite-role").value });
    notice((result.message || "Invitation sent.") + (result.user_id ? " Staff ID: " + result.user_id : ""), "success");
    byId("invite-email").value = "";
  } catch (error) { notice(error.message, "error"); }
  finally { busy(form, false); }
});

const recoveryParams = new URLSearchParams(location.search);
const resetUser = recoveryParams.get("userId") || "";
const resetToken = recoveryParams.get("secret") || "";
if (resetUser && resetToken) {
  resetUserID = resetUser;
  resetSecret = resetToken;
  showView("update-password");
  history.replaceState(null, "", "/reset-password");
}
loadSession();


byId("stepup-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget; busy(form, true);
  try {
    const result = await api("/api/admin/step-up", { password: byId("stepup-password").value });
    byId("stepup-password").value = "";
    byId("stepup-state").textContent = "Re-authenticated. Sensitive actions are unlocked until " + new Date(result.step_up_expires_at * 1000).toLocaleTimeString() + ".";
    if (currentUser && currentUser.role === "owner") loadStaffDirectory();
    notice("Identity confirmed. The step-up window expires in five minutes.", "success");
  } catch (error) {
    byId("stepup-state").textContent = "Re-authentication was not completed.";
    notice(error.message, "error");
  } finally { busy(form, false); }
});

byId("permission-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget; busy(form, true);
  try {
    const userID = byId("permission-user").value.trim();
    const result = await api("/api/admin/staff/" + encodeURIComponent(userID) + "/permissions", {
      permission: byId("permission-key").value,
      granted: byId("permission-granted").value === "true",
      reason: byId("permission-reason").value.trim()
    }, "PUT");
    notice((result.granted ? "Granted " : "Denied ") + result.permission + " for " + result.user_id + ".", "success");
    byId("permission-reason").value = "";
  } catch (error) { notice(error.message, "error"); }
  finally { busy(form, false); }
});

async function loadApprovalQueue() {
  const list = byId("approval-list");
  if (!list) return;
  list.replaceChildren();
  try {
    const data = await api("/api/admin/sensitive-actions");
    const items = data.items || [];
    if (!items.length) {
      const empty = document.createElement("p");
      empty.className = "field-help";
      empty.textContent = "No pending sensitive actions.";
      list.appendChild(empty);
      return;
    }
    for (const item of items) {
      const card = document.createElement("article");
      card.className = "approval-item";
      const heading = document.createElement("b");
      heading.textContent = item.action_type + " · " + item.target_reference;
      const detail = document.createElement("p");
      detail.textContent = item.reason + " · " + item.approval_count + "/" + item.required_approvals + " approvals";
      const requester = document.createElement("small");
      requester.textContent = "Requested by " + item.requested_by + " · expires " + new Date(item.expires_at).toLocaleString();
      card.append(heading, detail, requester);
      if (currentUser && currentUser.user_id !== item.requested_by) {
        const form = document.createElement("form");
        form.className = "form compact";
        const reason = document.createElement("input");
        reason.required = true;
        reason.minLength = 8;
        reason.maxLength = 1000;
        reason.placeholder = "Reason for approving this request";
        const button = document.createElement("button");
        button.type = "submit";
        button.className = "button button-primary";
        button.textContent = "Approve";
        form.append(reason, button);
        form.addEventListener("submit", async (event) => {
          event.preventDefault();
          button.disabled = true;
          try {
            const result = await api("/api/admin/sensitive-actions/" + encodeURIComponent(item.id) + "/approve", { reason: reason.value.trim() });
            notice("Approval recorded. Current state: " + result.status + " (" + result.approval_count + "/" + result.required_approvals + ").", "success");
            await loadApprovalQueue();
          } catch (error) {
            notice(error.message, "error");
            button.disabled = false;
          }
        });
        card.appendChild(form);
      } else if (currentUser && currentUser.user_id === item.requested_by) {
        const own = document.createElement("small");
        own.textContent = "You requested this action and cannot approve it.";
        card.appendChild(own);
      }
      list.appendChild(card);
    }
  } catch (error) {
    const failed = document.createElement("p");
    failed.className = "field-help";
    failed.textContent = error.message;
    list.appendChild(failed);
  }
}
byId("approval-refresh").addEventListener("click", loadApprovalQueue);


async function loadStaffDirectory() {
  const list = byId("staff-list");
  if (!list) return;
  list.replaceChildren();
  try {
    const data = await api("/api/admin/staff");
    for (const staff of (data.items || [])) {
      const card = document.createElement("article");
      card.className = "approval-item";
      const heading = document.createElement("b");
      heading.textContent = staff.user_id + " · " + staff.role;
      const detail = document.createElement("p");
      detail.textContent = staff.revoked_at ? "Access revoked " + new Date(staff.revoked_at).toLocaleString() : "Active staff access · updated " + new Date(staff.updated_at).toLocaleString();
      card.append(heading, detail);
      if (staff.role !== "owner" && !staff.revoked_at) {
        const form = document.createElement("form");
        form.className = "form compact";
        const select = document.createElement("select");
        for (const role of ["administrator", "engineer", "operations", "support", "moderator"]) {
          const option = document.createElement("option");
          option.value = role;
          option.textContent = role;
          option.selected = role === staff.role;
          select.appendChild(option);
        }
        const save = document.createElement("button");
        save.type = "submit";
        save.className = "button button-secondary";
        save.textContent = "Change role";
        form.append(select, save);
        form.addEventListener("submit", async (event) => {
          event.preventDefault();
          save.disabled = true;
          try {
            await api("/api/admin/staff/" + encodeURIComponent(staff.user_id) + "/role", { role: select.value }, "PUT");
            notice("Staff role updated.", "success");
            await loadStaffDirectory();
          } catch (error) { notice(error.message, "error"); save.disabled = false; }
        });
        const revoke = document.createElement("button");
        revoke.type = "button";
        revoke.className = "button button-quiet";
        revoke.textContent = "Revoke access";
        revoke.addEventListener("click", async () => {
          revoke.disabled = true;
          try {
            await api("/api/admin/staff/" + encodeURIComponent(staff.user_id), undefined, "DELETE");
            notice("Staff access revoked. The account can no longer enter the staff console.", "success");
            await loadStaffDirectory();
          } catch (error) { notice(error.message, "error"); revoke.disabled = false; }
        });
        card.append(form, revoke);
      }
      list.appendChild(card);
    }
    if (!(data.items || []).length) {
      const empty = document.createElement("p");
      empty.className = "field-help";
      empty.textContent = "No staff accounts have been invited yet.";
      list.appendChild(empty);
    }
  } catch (error) {
    const failed = document.createElement("p");
    failed.className = "field-help";
    failed.textContent = error.message;
    list.appendChild(failed);
  }
}
byId("staff-refresh").addEventListener("click", loadStaffDirectory);


byId("sensitive-request-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  const rawPayload = byId("sensitive-payload").value.trim();
  let payload = {};
  try {
    if (rawPayload) payload = JSON.parse(rawPayload);
    if (payload === null || Array.isArray(payload) || typeof payload !== "object") throw new Error("Payload must be a JSON object.");
  } catch (error) {
    notice("Check the structured payload: " + error.message, "error");
    return;
  }
  busy(form, true);
  try {
    const result = await api("/api/admin/sensitive-actions", {
      action_type: byId("sensitive-action-type").value,
      target_reference: byId("sensitive-target").value.trim(),
      reason: byId("sensitive-reason").value.trim(),
      payload
    });
    notice("Request recorded as " + result.id + ". Two authorised people must approve it; nothing has executed.", "success");
    form.reset();
    await loadApprovalQueue();
    await loadAuditHistory();
  } catch (error) { notice(error.message, "error"); }
  finally { busy(form, false); }
});

async function loadAuditHistory() {
  const list = byId("audit-list");
  if (!list) return;
  list.replaceChildren();
  try {
    const data = await api("/api/admin/audit");
    for (const entry of (data.items || [])) {
      const card = document.createElement("article");
      card.className = "approval-item";
      const heading = document.createElement("b");
      heading.textContent = entry.action + " · " + entry.target;
      const detail = document.createElement("p");
      detail.textContent = "Actor " + entry.actor + " · " + new Date(entry.created_at).toLocaleString();
      const metadata = document.createElement("small");
      metadata.textContent = JSON.stringify(entry.metadata || {});
      card.append(heading, detail, metadata);
      list.appendChild(card);
    }
    if (!(data.items || []).length) {
      const empty = document.createElement("p");
      empty.className = "field-help";
      empty.textContent = "No security audit events have been recorded.";
      list.appendChild(empty);
    }
  } catch (error) {
    const denied = document.createElement("p");
    denied.className = "field-help";
    denied.textContent = error.message;
    list.appendChild(denied);
  }
}
byId("audit-refresh").addEventListener("click", loadAuditHistory);
