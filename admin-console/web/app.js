const byId = (id) => document.getElementById(id);
const views = ["login", "signup", "recover", "update-password"];
let currentUser = null;
let resetAccessToken = "";
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
    signup: ["Request an account", "Create your identity; staff access is approved separately."],
    recover: ["Reset password", "We'll email you a secure recovery link if the account exists."],
    "update-password": ["Choose a new password", "Set a new password using your recovery link."]
  }[name];
  byId("auth-title").textContent = copy[0];
  byId("auth-subtitle").textContent = copy[1];
}
async function api(path, body) {
  const response = await fetch(path, {
    method: body === undefined ? "GET" : "POST",
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
  refreshOverview();
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
    const pending = metricValue(m, "queue_pending");
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
byId("sign-up-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget; busy(form, true);
  try {
    const result = await api("/api/auth/sign-up", { email: byId("signup-email").value, password: byId("signup-password").value });
    showView("login"); notice(result.message || "Account request created. Owner approval is required before staff access.", "success");
    byId("login-email").value = byId("signup-email").value;
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
    const result = await api("/api/auth/update-password", { access_token: resetAccessToken, password: byId("new-password").value });
    history.replaceState(null, "", "/");
    resetAccessToken = "";
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
    notice(result.message || "Invitation sent.", "success");
    byId("invite-email").value = "";
  } catch (error) { notice(error.message, "error"); }
  finally { busy(form, false); }
});

const hash = new URLSearchParams(location.hash.replace(/^#/, ""));
const authType = hash.get("type") || "";
if ((authType === "recovery" || authType === "invite") && hash.get("access_token")) {
  resetAccessToken = hash.get("access_token");
  showView("update-password");
  history.replaceState(null, "", "/reset-password");
} else if (authType === "signup") {
  history.replaceState(null, "", "/");
  showView("login");
  notice("Email confirmation received. You can now sign in; staff access still requires an approved role.", "success");
} else if (authType === "invite") {
  history.replaceState(null, "", "/");
  showView("login");
  notice("Invitation link detected. Please request a new invitation if this link has expired.", "info");
}
loadSession();
