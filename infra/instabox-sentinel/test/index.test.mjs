import test from "node:test";
import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { validateWebhookRequest, validateGitHubWebhookRequest, classifyGitHubFailure, reconcileStoppedEvent, verifyStartOperation, EventLedger } from "../src/index.mjs";

const secretBytes = Buffer.from("test-only-webhook-signing-key");
const secret = `whsec_${secretBytes.toString("base64")}`;
const now = 1_800_000_000;

function signedRequest({ body, timestamp = String(now), deliveryId = "evt_test_123", signatureBody = body } = {}) {
  const signature = createHmac("sha256", secretBytes)
    .update(`${deliveryId}.${timestamp}.${signatureBody}`)
    .digest("base64");
  return new Request("https://sentinel.test/webhooks/instabox", {
    method: "POST",
    headers: {
      "webhook-id": deliveryId,
      "webhook-timestamp": timestamp,
      "webhook-signature": `v1,${signature}`,
    },
    body,
  });
}

const eventBody = JSON.stringify({
  id: "evt_test_123",
  type: "sandbox.stopped",
  data: { sandbox: { id: "machine-123", name: "testagramdb" }, previousState: "stopping", state: "stopped" },
});

test("accepts a correctly signed InstaBox webhook", async () => {
  const result = await validateWebhookRequest(signedRequest({ body: eventBody }), secret, now);
  assert.equal(result.ok, true);
  assert.equal(result.event.type, "sandbox.stopped");
  assert.equal(result.event.data.sandbox.id, "machine-123");
});

test("rejects a tampered body", async () => {
  const request = signedRequest({ body: eventBody });
  const tampered = new Request(request.url, {
    method: "POST",
    headers: request.headers,
    body: eventBody.replace("stopped", "started"),
  });
  const result = await validateWebhookRequest(tampered, secret, now);
  assert.equal(result.ok, false);
  assert.equal(result.status, 401);
});

test("rejects stale webhook timestamps", async () => {
  const result = await validateWebhookRequest(
    signedRequest({ body: eventBody, timestamp: String(now - 301) }), secret, now,
  );
  assert.equal(result.ok, false);
  assert.equal(result.status, 400);
});

test("rejects a mismatched event and delivery ID", async () => {
  const body = JSON.stringify({ id: "evt_other", type: "sandbox.stopped", data: { sandbox: { id: "machine-123" } } });
  const result = await validateWebhookRequest(signedRequest({ body }), secret, now);
  assert.equal(result.ok, false);
  assert.equal(result.status, 400);
});

test("rejects a malformed signing secret", async () => {
  const result = await validateWebhookRequest(signedRequest({ body: eventBody }), "whsec_not-base64!", now);
  assert.equal(result.ok, false);
  assert.equal(result.status, 503);
});

test("accepts one matching signature among rotated signatures", async () => {
  const request = signedRequest({ body: eventBody });
  const signature = request.headers.get("webhook-signature");
  const rotated = new Request(request.url, {
    method: "POST",
    headers: { ...Object.fromEntries(request.headers), "webhook-signature": `v1,AAAA ${signature}` },
    body: eventBody,
  });
  const result = await validateWebhookRequest(rotated, secret, now);
  assert.equal(result.ok, true);
});


async function withMockInstaBox(machine, callback) {
  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (input, init = {}) => {
    const url = new URL(String(input));
    calls.push({ method: init.method || "GET", path: url.pathname, headers: new Headers(init.headers) });
    if (url.pathname === "/v1/machines") return Response.json({ machines: [{ id: "machine-123", name: "testagramdb" }], nextCursor: null });
    if (url.pathname === "/v1/machines/machine-123" && (init.method || "GET") === "GET") return Response.json({ machine });
    if (url.pathname === "/v1/operations/op-123" && (init.method || "GET") === "GET") return Response.json({ operation: { id: "op-123", state: "queued" } });
    if (url.pathname === "/v1/machines/machine-123/start" && init.method === "POST") return Response.json({ operation: { id: "op-123", state: "queued" } }, { status: 202 });
    throw new Error("Unexpected mocked InstaBox request: " + (init.method || "GET") + " " + url.pathname);
  };
  try { await callback(calls); } finally { globalThis.fetch = originalFetch; }
}

const stoppedEvent = { type: "sandbox.stopped", data: { sandbox: { id: "machine-123", name: "testagramdb" }, state: "stopped" } };

test("starts only the exact VPS when stopped and desiredState remains running", async () => {
  await withMockInstaBox({ id: "machine-123", name: "testagramdb", state: "stopped", desiredState: "running", operationId: null }, async (calls) => {
    const result = await reconcileStoppedEvent({ INSTABOX_API_KEY: "unit-test-only", INSTABOX_MACHINE_NAME: "testagramdb" }, stoppedEvent, "evt_start_123");
    assert.equal(result.action, "start_requested");
    const start = calls.find((call) => call.path.endsWith("/start"));
    assert.ok(start);
    assert.equal(start.headers.get("Idempotency-Key"), "testagram-sentinel-evt_start_123");
    assert.equal(start.headers.get("Authorization"), "Bearer unit-test-only");
  });
});

test("never wakes an intentionally stopped machine", async () => {
  await withMockInstaBox({ id: "machine-123", name: "testagramdb", state: "stopped", desiredState: "stopped", operationId: null }, async (calls) => {
    const result = await reconcileStoppedEvent({ INSTABOX_API_KEY: "unit-test-only", INSTABOX_MACHINE_NAME: "testagramdb" }, stoppedEvent, "evt_operator_stop");
    assert.equal(result.action, "ignored");
    assert.match(result.reason, /desiredState is not running/);
    assert.equal(calls.some((call) => call.path.endsWith("/start")), false);
  });
});

test("never starts a running machine or one with an active operation", async () => {
  for (const machine of [
    { id: "machine-123", name: "testagramdb", state: "running", desiredState: "running", operationId: null },
    { id: "machine-123", name: "testagramdb", state: "stopped", desiredState: "running", operationId: "op-active" },
  ]) {
    await withMockInstaBox(machine, async (calls) => {
      const result = await reconcileStoppedEvent({ INSTABOX_API_KEY: "unit-test-only", INSTABOX_MACHINE_NAME: "testagramdb" }, stoppedEvent, "evt_noop_123");
      assert.equal(result.action, "ignored");
      assert.equal(calls.some((call) => call.path.endsWith("/start")), false);
    });
  }
});


test("accepts and safely ignores signed non-stop lifecycle events", async () => {
  const body = JSON.stringify({ id: "evt_started_123", type: "sandbox.started", data: { sandbox: { id: "machine-123" } } });
  const result = await validateWebhookRequest(signedRequest({ body, deliveryId: "evt_started_123" }), secret, now);
  assert.equal(result.ok, true);
  assert.equal(result.event.type, "sandbox.started");
});

test("rejects stopped events without a sandbox identity", async () => {
  const body = JSON.stringify({ id: "evt_missing_machine", type: "sandbox.stopped", data: {} });
  const result = await validateWebhookRequest(signedRequest({ body, deliveryId: "evt_missing_machine" }), secret, now);
  assert.equal(result.ok, false);
  assert.equal(result.status, 400);
});


test("persists a verified delivery before acknowledging InstaBox", async () => {
  const records = new Map();
  let alarmAt = null;
  const state = {
    storage: {
      get: async (key) => records.get(key),
      put: async (key, value) => records.set(key, value),
      setAlarm: async (value) => { alarmAt = value; },
      list: async () => records,
      delete: async (key) => records.delete(key),
    },
    blockConcurrencyWhile: async (callback) => callback(),
  };
  const ledger = new EventLedger(state, {});
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => { throw new Error("provider I/O must not run before the webhook acknowledgement"); };
  try {
    const response = await ledger.fetch(new Request("https://ledger/process", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ deliveryId: "evt_durable_123", event: stoppedEvent }),
    }));
    assert.equal(response.status, 202);
    assert.equal(records.get("event:evt_durable_123").status, "pending");
    assert.equal(records.get("event:evt_durable_123").attempts, 0);
    assert.ok(Number.isFinite(alarmAt));
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("re-acknowledges pending duplicate deliveries without resetting their retry state", async () => {
  const records = new Map([["event:evt_pending_123", {
    status: "pending", deliveryId: "evt_pending_123", event: stoppedEvent, attempts: 3, updatedAt: 1,
  }]]);
  let alarmAt = null;
  const state = {
    storage: {
      get: async (key) => records.get(key),
      put: async (key, value) => records.set(key, value),
      setAlarm: async (value) => { alarmAt = value; },
      list: async () => records,
      delete: async (key) => records.delete(key),
    },
    blockConcurrencyWhile: async (callback) => callback(),
  };
  const ledger = new EventLedger(state, {});
  const response = await ledger.fetch(new Request("https://ledger/process", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ deliveryId: "evt_pending_123", event: stoppedEvent }),
  }));
  assert.equal(response.status, 202);
  assert.equal(records.get("event:evt_pending_123").attempts, 3);
  assert.ok(Number.isFinite(alarmAt));
});


test("provider operation pending is not treated as completed recovery", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (input) => {
    const url = new URL(String(input));
    if (url.pathname === "/v1/operations/op-123") return Response.json({ operation: { id: "op-123", state: "running" } });
    throw new Error("Unexpected request: " + url.pathname);
  };
  try {
    const result = await verifyStartOperation(
      { INSTABOX_API_KEY: "unit-test-only", INSTABOX_MACHINE_NAME: "testagramdb" },
      { operationId: "op-123", machineId: "machine-123", startedAt: now },
      now + 5_000,
    );
    assert.equal(result.done, false);
    assert.equal(result.state, "running");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("provider success is not accepted until an independent machine read confirms running", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (input) => {
    const url = new URL(String(input));
    if (url.pathname === "/v1/operations/op-123") return Response.json({ operation: { id: "op-123", state: "succeeded" } });
    if (url.pathname === "/v1/machines/machine-123") return Response.json({ machine: {
      id: "machine-123", name: "testagramdb", state: "running", desiredState: "running",
    } });
    throw new Error("Unexpected request: " + url.pathname);
  };
  try {
    const result = await verifyStartOperation(
      { INSTABOX_API_KEY: "unit-test-only", INSTABOX_MACHINE_NAME: "testagramdb" },
      { operationId: "op-123", machineId: "machine-123", startedAt: now },
      now + 5_000,
    );
    assert.equal(result.done, true);
    assert.equal(result.result.providerState, "running");
    assert.equal(result.result.machineId, "machine-123");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("Durable Object retains starting state until provider lifecycle verification passes", async () => {
  const records = new Map([["event:evt_verify_123", {
    status: "pending", deliveryId: "evt_verify_123", event: stoppedEvent, attempts: 0, updatedAt: now,
  }]]);
  let alarmAt = null;
  const state = {
    storage: {
      get: async (key) => records.get(key),
      put: async (key, value) => records.set(key, value),
      setAlarm: async (value) => { alarmAt = value; },
      list: async () => records,
      delete: async (key) => records.delete(key),
    },
    blockConcurrencyWhile: async (callback) => callback(),
  };
  await withMockInstaBox({
    id: "machine-123", name: "testagramdb", state: "stopped", desiredState: "running", operationId: null,
  }, async (calls) => {
    const ledger = new EventLedger(state, { INSTABOX_API_KEY: "unit-test-only", INSTABOX_MACHINE_NAME: "testagramdb" });
    await ledger.alarm();
    const record = records.get("event:evt_verify_123");
    assert.equal(record.status, "starting");
    assert.equal(record.operationId, "op-123");
    assert.equal(calls.filter((call) => call.path.endsWith("/start")).length, 1);
    assert.ok(Number.isFinite(alarmAt));
    await ledger.alarm();
    assert.equal(records.get("event:evt_verify_123").status, "starting");
    assert.equal(calls.filter((call) => call.path.endsWith("/start")).length, 1);
  });
});

test("provider verification deadline fails closed", async () => {
  await assert.rejects(
    verifyStartOperation(
      { INSTABOX_API_KEY: "unit-test-only", INSTABOX_MACHINE_NAME: "testagramdb" },
      { operationId: "op-123", machineId: "machine-123", startedAt: now },
      now + 10 * 60_000 + 1,
    ),
    /ten-minute verification deadline/,
  );
});


const githubSecret = "test-only-github-webhook-secret-at-least-32";
function signedGitHubRequest(eventType, payload, deliveryId = "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11") {
  const body = JSON.stringify(payload);
  const signature = createHmac("sha256", githubSecret).update(body).digest("hex");
  return new Request("https://sentinel.test/webhooks/github", {
    method: "POST",
    headers: {
      "x-github-event": eventType,
      "x-github-delivery": deliveryId,
      "x-hub-signature-256": `sha256=${signature}`,
      "content-type": "application/json",
    },
    body,
  });
}

const failedBackendWorkflow = {
  action: "completed",
  repository: { full_name: "Trendyzima/Testagram-backend-" },
  workflow: { name: "Deploy Backend Production" },
  workflow_run: {
    id: 12345,
    name: "Deploy Backend Production",
    conclusion: "failure",
    head_sha: "abc123",
    head_branch: "main",
    html_url: "https://github.com/Trendyzima/Testagram-backend-/actions/runs/12345",
  },
};

test("accepts correctly signed GitHub failure webhook for an approved Testagram repository", async () => {
  const result = await validateGitHubWebhookRequest(
    signedGitHubRequest("workflow_run", failedBackendWorkflow),
    githubSecret,
  );
  assert.equal(result.ok, true);
  assert.equal(result.eventType, "workflow_run");
  assert.equal(result.payload.repository.full_name, "Trendyzima/Testagram-backend-");
});

test("rejects a tampered GitHub webhook body", async () => {
  const request = signedGitHubRequest("workflow_run", failedBackendWorkflow);
  const tampered = new Request(request.url, {
    method: "POST",
    headers: request.headers,
    body: JSON.stringify({ ...failedBackendWorkflow, action: "requested" }),
  });
  const result = await validateGitHubWebhookRequest(tampered, githubSecret);
  assert.equal(result.ok, false);
  assert.equal(result.status, 401);
});

test("rejects GitHub webhooks from repositories outside the Testagram allowlist", async () => {
  const result = await validateGitHubWebhookRequest(
    signedGitHubRequest("workflow_run", {
      ...failedBackendWorkflow,
      repository: { full_name: "attacker/other-project" },
    }),
    githubSecret,
  );
  assert.equal(result.ok, false);
  assert.equal(result.status, 403);
});

test("classifies production deployment failures as P1 without authorizing mutations", () => {
  const result = classifyGitHubFailure("workflow_run", failedBackendWorkflow, "delivery-123");
  assert.equal(result.action, "triage");
  assert.equal(result.organ, "Backend");
  assert.equal(result.severity, "P1");
  assert.equal(result.automaticMutation, false);
  assert.equal(result.runUrl, failedBackendWorkflow.workflow_run.html_url);
});

test("classifies non-production feature CI failures as P2 and ignores successful runs", () => {
  const featureFailure = {
    ...failedBackendWorkflow,
    repository: { full_name: "Trendyzima/testagram-flutter-client-" },
    workflow: { name: "Flutter Unit Tests" },
    workflow_run: { ...failedBackendWorkflow.workflow_run, name: "Flutter Unit Tests", conclusion: "failure" },
  };
  const failure = classifyGitHubFailure("workflow_run", featureFailure, "delivery-456");
  assert.equal(failure.action, "triage");
  assert.equal(failure.organ, "Flutter Client");
  assert.equal(failure.severity, "P2");
  const success = classifyGitHubFailure("workflow_run", {
    ...featureFailure,
    workflow_run: { ...featureFailure.workflow_run, conclusion: "success" },
  }, "delivery-789");
  assert.equal(success.action, "ignored");
});

test("Durable Object records GitHub incident triage without taking production action", async () => {
  const incident = classifyGitHubFailure("workflow_run", failedBackendWorkflow, "delivery-durable-123");
  const records = new Map([["event:delivery-durable-123", {
    status: "pending",
    deliveryId: "delivery-durable-123",
    event: { source: "github", incident },
    attempts: 0,
    updatedAt: now,
  }]]);
  const state = {
    storage: {
      get: async (key) => records.get(key),
      put: async (key, value) => records.set(key, value),
      setAlarm: async () => {},
      list: async () => records,
      delete: async (key) => records.delete(key),
    },
    blockConcurrencyWhile: async (callback) => callback(),
  };
  const ledger = new EventLedger(state, {});
  await ledger.alarm();
  assert.equal(records.get("event:delivery-durable-123").status, "complete");
  assert.equal(records.get("event:delivery-durable-123").result.action, "triage");
  assert.equal(records.get("event:delivery-durable-123").result.automaticMutation, false);
});
