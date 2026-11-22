const API_BASE = "https://api.instabox.sh";
const EVENT_TTL_MS = 7 * 24 * 60 * 60 * 1000;
const MAX_EVENT_ATTEMPTS = 8;
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

  const declaredLength = Number(request.headers.get("content-length") || 0);
  if (Number.isFinite(declaredLength) && declaredLength > 65_536) {
    return { ok: false, status: 413, error: "webhook body exceeds the size limit" };
  }
  const rawBytes = new Uint8Array(await request.arrayBuffer());
  if (rawBytes.byteLength > 65_536) {
    return { ok: false, status: 413, error: "webhook body exceeds the size limit" };
  }
  let rawBody;
  try {
    rawBody = new TextDecoder("utf-8", { fatal: true }).decode(rawBytes);
  } catch {
    return { ok: false, status: 400, error: "webhook body is not valid UTF-8" };
  }
  const prefix = new TextEncoder().encode(`${deliveryId}.${timestampText}.`);
  const signedBytes = new Uint8Array(prefix.byteLength + rawBytes.byteLength);
  signedBytes.set(prefix);
  signedBytes.set(rawBytes, prefix.byteLength);
  const key = await crypto.subtle.importKey("raw", keyBytes, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const signed = await crypto.subtle.sign("HMAC", key, signedBytes);
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
  if (!event || typeof event !== "object" || Array.isArray(event) || typeof event.type !== "string") {
    return { ok: false, status: 400, error: "webhook event has an invalid shape" };
  }
  if (event.type === "sandbox.stopped" && !event.data?.sandbox?.id) {
    return { ok: false, status: 400, error: "stopped event is missing its sandbox identity" };
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
  if (!env.INSTABOX_API_KEY) throw new Error("InstaBox API key is not configured");
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

export async function reconcileStoppedEvent(env, event, deliveryId) {
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

  const startResponse = await instaboxRequest(env, `/v1/machines/${encodeURIComponent(machine.id)}/start`, {
    method: "POST",
    body: {},
    // Stable across webhook retries; InstaBox deduplicates the lifecycle request.
    idempotencyKey: `testagram-sentinel-${deliveryId}`,
  });
  const operation = unwrap(startResponse, "operation");
  if (!operation?.id) {
    // Some provider responses are synchronous. Accept them only after an
    // independent machine read confirms the exact VPS is already running.
    const finalSnapshot = unwrap(
      await instaboxRequest(env, `/v1/machines/${encodeURIComponent(machine.id)}`),
      "machine",
    );
    if (String(finalSnapshot.id) !== String(machine.id) ||
        finalSnapshot.name !== (env.INSTABOX_MACHINE_NAME || "testagramdb") ||
        finalSnapshot.state !== "running") {
      throw new Error("InstaBox returned no operation ID and the independent machine read did not confirm running");
    }
    return { action: "started", machineId: machine.id, eventId: deliveryId, providerState: "running" };
  }
  return {
    action: "start_requested",
    machineId: machine.id,
    eventId: deliveryId,
    operationId: String(operation.id),
  };
}

export async function verifyStartOperation(env, record, now = Date.now()) {
  const startedAt = Number(record.startedAt);
  if (!Number.isFinite(startedAt) || startedAt <= 0) {
    throw new Error("Recovery record is missing a valid operation start time");
  }
  if (now - startedAt > 10 * 60_000) {
    throw new Error("InstaBox start operation exceeded the ten-minute verification deadline");
  }
  if (!record.operationId || !record.machineId) {
    throw new Error("Recovery record is missing its provider operation or machine identity");
  }
  const operation = unwrap(
    await instaboxRequest(env, `/v1/operations/${encodeURIComponent(record.operationId)}`),
    "operation",
  );
  if (String(operation.id) !== String(record.operationId)) {
    throw new Error("InstaBox returned a different lifecycle operation");
  }
  if (operation.state === "failed") {
    throw new Error("InstaBox start operation failed");
  }
  if (operation.state !== "succeeded") {
    return { done: false, state: String(operation.state || "unknown") };
  }
  const machine = unwrap(
    await instaboxRequest(env, `/v1/machines/${encodeURIComponent(record.machineId)}`),
    "machine",
  );
  if (String(machine.id) !== String(record.machineId) ||
      machine.name !== (env.INSTABOX_MACHINE_NAME || "testagramdb")) {
    throw new Error("Final machine identity did not match the configured Testagram VPS");
  }
  if (machine.state !== "running" || machine.desiredState !== "running") {
    return { done: false, state: String(machine.state || "unknown") };
  }
  return {
    done: true,
    result: {
      action: "started",
      machineId: String(machine.id),
      operationId: String(operation.id),
      providerState: String(machine.state),
      verifiedAt: now,
    },
  };
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

    // Persist before acknowledging InstaBox. The alarm performs provider I/O
    // asynchronously, so a transient Durable Object failure returns non-2xx
    // and lets InstaBox retry instead of losing an already-acknowledged event.
    const result = await this.state.blockConcurrencyWhile(async () => {
      const key = `event:${event.deliveryId}`;
      const record = await this.state.storage.get(key);
      if (record?.status === "complete" || record?.status === "failed") {
        return { duplicate: true, status: record.status, result: record.result };
      }
      if (!record) {
        await this.state.storage.put(key, {
          status: "pending",
          deliveryId: event.deliveryId,
          event: event.event,
          attempts: 0,
          updatedAt: Date.now(),
        });
      }
      await this.state.storage.setAlarm(Date.now() + 1_000);
      return { duplicate: Boolean(record), status: "pending", queued: true };
    });
    return json(result, 202);
  }

  async alarm() {
    await this.state.blockConcurrencyWhile(async () => {
      const entries = await this.state.storage.list({ prefix: "event:" });
      let retryDelay = 0;
      for (const [key, record] of entries) {
        if (record?.status !== "pending" && record?.status !== "starting") {
          if (record?.expiresAt && record.expiresAt < Date.now()) await this.state.storage.delete(key);
          continue;
        }
        try {
          if (record.status === "starting") {
            const verification = await verifyStartOperation(this.env, record);
            if (!verification.done) {
              retryDelay = Math.max(retryDelay, 5_000);
              continue;
            }
            await this.state.storage.put(key, {
              status: "complete",
              deliveryId: record.deliveryId,
              result: verification.result,
              attempts: record.attempts || 0,
              updatedAt: Date.now(),
              expiresAt: Date.now() + EVENT_TTL_MS,
            });
            console.log("Testagram Sentinel independently verified VPS recovery", {
              eventId: record.deliveryId,
              machineId: verification.result.machineId,
              operationId: verification.result.operationId,
              providerState: verification.result.providerState,
            });
            continue;
          }

          const result = await reconcileStoppedEvent(this.env, record.event, record.deliveryId);
          if (result.action === "start_requested") {
            await this.state.storage.put(key, {
              ...record,
              status: "starting",
              machineId: result.machineId,
              operationId: result.operationId,
              startedAt: Date.now(),
              attempts: 0,
              updatedAt: Date.now(),
            });
            retryDelay = Math.max(retryDelay, 5_000);
          } else {
            await this.state.storage.put(key, {
              status: "complete",
              deliveryId: record.deliveryId,
              result,
              updatedAt: Date.now(),
              expiresAt: Date.now() + EVENT_TTL_MS,
            });
          }
        } catch (error) {
          const attempts = (record.attempts || 0) + 1;
          const exhausted = attempts >= MAX_EVENT_ATTEMPTS;
          const message = error instanceof Error ? error.message : "unknown error";
          await this.state.storage.put(key, {
            ...record,
            status: exhausted ? "failed" : record.status,
            attempts,
            lastError: message,
            updatedAt: Date.now(),
          });
          if (exhausted) {
            console.error("Testagram Sentinel stopped retrying an event after the retry limit", {
              eventId: record.deliveryId,
              attempts,
              error: message,
            });
          } else {
            retryDelay = Math.max(
              retryDelay,
              Math.min(30 * 60_000, 30_000 * (2 ** (attempts - 1))),
            );
            console.warn("Testagram Sentinel recovery verification/provider call failed", {
              eventId: record.deliveryId,
              attempts,
              phase: record.status,
              error: message,
            });
          }
        }
      }
      if (retryDelay) await this.state.storage.setAlarm(Date.now() + retryDelay);
    });
  }
}

export default {
  async fetch(request, env) {
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
    try {
      const persisted = await ledger.fetch("https://ledger/process", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ deliveryId: verified.deliveryId, event: verified.event }),
      });
      if (!persisted.ok) {
        console.error("Testagram Sentinel could not persist a verified delivery", { eventId: verified.deliveryId, status: persisted.status });
        return json({ error: "recovery event could not be persisted" }, 503);
      }
      return json({ accepted: true, action: "queued" }, 202);
    } catch (error) {
      console.error("Testagram Sentinel Durable Object persistence failed", {
        eventId: verified.deliveryId,
        error: error instanceof Error ? error.message : "unknown error",
      });
      return json({ error: "recovery event could not be persisted" }, 503);
    }
  },
};
