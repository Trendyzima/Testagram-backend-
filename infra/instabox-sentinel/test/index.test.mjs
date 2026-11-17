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
