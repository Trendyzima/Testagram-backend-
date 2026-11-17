const API_BASE = "https://api.instabox.sh";
const EVENT_TTL_MS = 7 * 24 * 60 * 60 * 1000;
const MAX_CLOCK_SKEW_SECONDS = 300;

function json(data, status = 200) {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" },
  });
}

function decodeBase64(value) {
  try {
    const binary = atob(value);
    return Uint8Array.from(binary, (character) => character.charCodeAt(0));
  } catch {
    return null;
  }
}

function constantTimeEqual(left, right) {
  if (left.length !== right.length) return false;
  let difference = 0;
  for (let index = 0; index < left.length; index += 1) {
    difference |= left[index] ^ right[index];
  }
  return difference === 0;
}

export async function validateWebhookRequest(request, secret, nowSeconds = Math.floor(Date.now() / 1000)) {
  if (!secret || !secret.startsWith("whsec_")) {
    return { ok: false, status: 503, error: "webhook verifier is not configured" };
  }

  const deliveryId = request.headers.get("webhook-id");
  const timestampText = request.headers.get("webhook-timestamp");
  const signatureHeader = request.headers.get("webhook-signature");
  if (!deliveryId || !timestampText || !signatureHeader) {
    return { ok: false, status: 400, error: "required webhook signature headers are missing" };
  }

  if (!/^\d{1,12}$/.test(timestampText)) {
    return { ok: false, status: 400, error: "invalid webhook timestamp" };
  }
  const timestamp = Number(timestampText);
  if (!Number.isSafeInteger(timestamp) || Math.abs(nowSeconds - timestamp) > MAX_CLOCK_SKEW_SECONDS) {
    return { ok: false, status: 400, error: "webhook timestamp is outside the accepted window" };
  }

  const keyBytes = decodeBase64(secret.slice("whsec_".length));
  if (!keyBytes?.length) {
    return { ok: false, status: 503, error: "webhook signing secret is malformed" };
  }

  const rawBody = await request.text();
  const encoder = new TextEncoder();
  const key = await crypto.subtle.importKey("raw", keyBytes, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const signed = await crypto.subtle.sign("HMAC", key, encoder.encode(`${deliveryId}.${timestampText}.${rawBody}`));
  const expected = new Uint8Array(signed);
  const signatures = signatureHeader.split(/\s+/).map((item) => item.split(",", 2))
    .filter(([version, value]) => version === "v1" && value)
    .map(([, value]) => decodeBase64(value))
    .filter(Boolean);
  if (!signatures.some((candidate) => constantTimeEqual(candidate, expected))) {
    return { ok: false, status: 401, error: "webhook signature verification failed" };
  }

  let event;
  try {
    event = JSON.parse(rawBody);
  } catch {
    return { ok: false, status: 400, error: "webhook body is not valid JSON" };
  }
  if (!event || typeof event !== "object" || typeof event.type !== "string" || !event.data?.sandbox?.id) {
    return { ok: false, status: 400, error: "webhook event has an invalid shape" };
  }
  if (event.id && event.id !== deliveryId) {
    return { ok: false, status: 400, error: "webhook event ID does not match delivery header" };
  }
  return { ok: true, event, deliveryId, rawBody };
}

function unwrap(payload, key) {
  if (key && payload?.[key] && typeof payload[key] === "object") return payload[key];
  if (payload?.data && typeof payload.data === "object") {
    if (key && payload.data[key] && typeof payload.data[key] === "object") return payload.data[key];
    return payload.data;
  }
  return payload;
}

async function instaboxRequest(env, path, options = {}) {
  const headers = new Headers({ accept: "application/json", authorization: `Bearer ${env.INSTABOX_API_KEY}` });
  if (options.body !== undefined) headers.set("content-type", "application/json");
  if (options.idempotencyKey) headers.set("Idempotency-Key", options.idempotencyKey);
  const response = await fetch(`${API_BASE}${path}`, {
    method: options.method || "GET",
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: AbortSignal.timeout(8000),
  });
  const text = await response.text();
  if (!response.ok) {
    // Do not include response bodies: provider errors can contain sensitive details.
    throw new Error(`InstaBox API returned HTTP ${response.status}`);
  }
  if (!text) return {};
  try {
    return JSON.parse(text);
  } catch {
    throw new Error("InstaBox API returned malformed JSON");
  }
}

async function resolveMachine(env, expectedMachineId) {
  const machineName = env.INSTABOX_MACHINE_NAME || "testagramdb";
  let cursor;
  const matches = [];
  for (let pageNumber = 0; pageNumber < 10; pageNumber += 1) {
    const query = new URLSearchParams({ limit: "100" });
    if (cursor) query.set("cursor", cursor);
    const response = unwrap(await instaboxRequest(env, `/v1/machines?${query.toString()}`));
    if (!Array.isArray(response.machines)) throw new Error("InstaBox machine list is missing its machines array");
    matches.push(...response.machines.filter((machine) => machine?.name === machineName));
    cursor = response.nextCursor;
    if (!cursor) break;
    if (pageNumber === 9) throw new Error("InstaBox machine list exceeded the safe pagination limit");
  }
  if (matches.length !== 1 || !matches[0]?.id) {
    throw new Error(`Expected exactly one InstaBox machine named ${machineName}; found ${matches.length}`);
  }
  if (String(matches[0].id) !== String(expectedMachineId)) {
    throw new Error("Webhook machine ID does not match the uniquely resolved Testagram VPS");
  }
  return matches[0];
}

async function reconcileStoppedEvent(env, event, deliveryId) {
  if (event.type !== "sandbox.stopped") return { action: "ignored", reason: "event is not sandbox.stopped" };
  const eventMachine = event.data.sandbox;
  const machine = await resolveMachine(env, eventMachine.id);
  const snapshot = unwrap(await instaboxRequest(env, `/v1/machines/${encodeURIComponent(machine.id)}`), "machine");
  if (String(snapshot.id) !== String(machine.id) || snapshot.name !== (env.INSTABOX_MACHINE_NAME || "testagramdb")) {
    throw new Error("InstaBox machine snapshot identity did not match the configured Testagram VPS");
  }
  if (snapshot.state !== "stopped") {
    return { action: "ignored", reason: `current machine state is ${String(snapshot.state)}` };
  }
  if (snapshot.desiredState !== "running") {
    return { action: "ignored", reason: "desiredState is not running; respecting operator intent" };
  }
  if (snapshot.operationId) {
    return { action: "ignored", reason: "machine already has an active lifecycle operation" };
  }

  await instaboxRequest(env, `/v1/machines/${encodeURIComponent(machine.id)}/start`, {
    method: "POST",
    body: {},
    // Stable across webhook retries; InstaBox deduplicates the lifecycle request.
    idempotencyKey: `testagram-sentinel-${deliveryId}`,
  });
  return { action: "start_requested", machineId: machine.id, eventId: deliveryId };
}

export class EventLedger {
  constructor(state, env) {
    this.state = state;
    this.env = env;
  }

  async fetch(request) {
    if (request.method !== "POST") return json({ error: "method not allowed" }, 405);
    let event;
    try {
      event = await request.json();
    } catch {
      return json({ error: "invalid internal event" }, 400);
    }
    if (!event?.deliveryId || !event?.event) return json({ error: "invalid internal event" }, 400);

    const result = await this.state.blockConcurrencyWhile(async () => {
      const key = `event:${event.deliveryId}`;
      const record = await this.state.storage.get(key);
      if (record?.status === "complete") return { duplicate: true, result: record.result };

      const pending = { status: "pending", deliveryId: event.deliveryId, event: event.event, updatedAt: Date.now() };
      await this.state.storage.put(key, pending);
      try {
        const outcome = await reconcileStoppedEvent(this.env, event.event, event.deliveryId);
        await this.state.storage.put(key, {
          status: "complete", deliveryId: event.deliveryId, result: outcome,
          updatedAt: Date.now(), expiresAt: Date.now() + EVENT_TTL_MS,
        });
        return { duplicate: false, result: outcome };
      } catch (error) {
        await this.state.storage.put(key, { ...pending, lastError: error instanceof Error ? error.message : "unknown error" });
        await this.state.storage.setAlarm(Date.now() + 30_000);
        return { duplicate: false, retryScheduled: true };
      }
    });
    return json(result, 202);
  }

  async alarm() {
    await this.state.blockConcurrencyWhile(async () => {
      const entries = await this.state.storage.list({ prefix: "event:" });
      let retryPending = false;
      for (const [key, record] of entries) {
        if (record?.status !== "pending") {
          if (record?.expiresAt && record.expiresAt < Date.now()) await this.state.storage.delete(key);
          continue;
        }
        try {
          const result = await reconcileStoppedEvent(this.env, record.event, record.deliveryId);
          await this.state.storage.put(key, {
            status: "complete", deliveryId: record.deliveryId, result,
            updatedAt: Date.now(), expiresAt: Date.now() + EVENT_TTL_MS,
          });
        } catch (error) {
          retryPending = true;
          await this.state.storage.put(key, {
            ...record, lastError: error instanceof Error ? error.message : "unknown error",
            updatedAt: Date.now(),
          });
        }
      }
      if (retryPending) await this.state.storage.setAlarm(Date.now() + 60_000);
    });
  }
}

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (request.method === "GET" && url.pathname === "/healthz") {
      return json({ service: "testagram-instabox-sentinel", status: "ok" });
    }
    if (url.pathname !== "/webhooks/instabox" || request.method !== "POST") {
      return json({ error: "not found" }, 404);
    }

    const verified = await validateWebhookRequest(request, env.INSTABOX_WEBHOOK_SECRET);
    if (!verified.ok) return json({ error: verified.error }, verified.status);
    if (verified.event.type !== "sandbox.stopped") return json({ accepted: true, action: "ignored" }, 202);
    if (verified.event.data.sandbox.name &&
        verified.event.data.sandbox.name !== (env.INSTABOX_MACHINE_NAME || "testagramdb")) {
      return json({ accepted: true, action: "ignored", reason: "machine name does not match" }, 202);
    }

    const machineId = String(verified.event.data.sandbox.id);
    const ledger = env.EVENT_LEDGER.get(env.EVENT_LEDGER.idFromName(machineId));
    ctx.waitUntil(ledger.fetch("https://ledger/process", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ deliveryId: verified.deliveryId, event: verified.event }),
    }).catch(() => {
      // The durable object's alarm retries pending events; never log credentials or payloads.
    }));
    return json({ accepted: true, action: "queued" }, 202);
  },
};
