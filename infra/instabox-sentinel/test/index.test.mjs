import test from "node:test";
import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { validateWebhookRequest } from "./index.mjs";

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

import { reconcileStoppedEvent } from "../src/index.mjs";

async function withMockInstaBox(machine, callback) {
  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (input, init = {}) => {
    const url = new URL(String(input));
    calls.push({ method: init.method || "GET", path: url.pathname, headers: new Headers(init.headers) });
    if (url.pathname === "/v1/machines") return Response.json({ machines: [{ id: "machine-123", name: "testagramdb" }], nextCursor: null });
    if (url.pathname === "/v1/machines/machine-123" && (init.method || "GET") === "GET") return Response.json({ machine });
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
