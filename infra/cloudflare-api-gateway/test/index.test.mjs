import test from "node:test";
import assert from "node:assert/strict";
import gateway from "../src/index.mjs";

const APP_ORIGIN = "https://8443-agq4me2xgrgx5ge52ts437tqea.instabox.tech";

function apiRequest(path, init = {}) {
  return new Request("https://api.testagram.site" + path, init);
}

test("forwards API path, query, auth, body and idempotency without trusting client forwarding headers", async () => {
  const originalFetch = globalThis.fetch;
  let upstreamRequest;

  globalThis.fetch = async (request) => {
    upstreamRequest = request;
    return new Response('{"ok":true}', {
      status: 201,
      headers: { "content-type": "application/json" },
    });
  };

  try {
    const request = apiRequest("/api/posts?visibility=public", {
      method: "POST",
      headers: {
        authorization: "Bearer test-token",
        "content-type": "application/json",
        "idempotency-key": "post-create-123",
        "cf-connecting-ip": "198.51.100.24",
        "cf-ray": "test-ray-123",
        "x-forwarded-for": "203.0.113.9",
        "x-forwarded-host": "attacker.example",
        "x-forwarded-proto": "http",
        "x-real-ip": "203.0.113.9",
        forwarded: "for=203.0.113.9;host=attacker.example",
        connection: "keep-alive, x-remove-me",
        "x-remove-me": "untrusted",
      },
      body: '{"text":"hello"}',
    });

    const response = await gateway.fetch(request, { APP_ORIGIN, EDGE_PROXY_SHARED_TOKEN: "a".repeat(64) });
    assert.equal(response.status, 201);
    assert.equal(await response.text(), '{"ok":true}');
    assert.equal(upstreamRequest.url, APP_ORIGIN + "/api/posts?visibility=public");
    assert.equal(upstreamRequest.method, "POST");
    assert.equal(await upstreamRequest.text(), '{"text":"hello"}');
    assert.equal(upstreamRequest.headers.get("authorization"), "Bearer test-token");
    assert.equal(upstreamRequest.headers.get("idempotency-key"), "post-create-123");
    assert.equal(upstreamRequest.headers.get("x-forwarded-for"), "198.51.100.24");
    assert.equal(upstreamRequest.headers.get("x-real-ip"), "198.51.100.24");
    assert.equal(upstreamRequest.headers.get("x-forwarded-host"), "api.testagram.site");
    assert.equal(upstreamRequest.headers.get("x-forwarded-proto"), "https");
    assert.equal(upstreamRequest.headers.get("x-testagram-edge-token"), "a".repeat(64));
    assert.equal(upstreamRequest.headers.get("forwarded"), null);
    assert.equal(upstreamRequest.headers.get("x-remove-me"), null);
    assert.equal(response.headers.get("cache-control"), "no-store");
    assert.equal(response.headers.get("x-testagram-edge"), "cloudflare-api-gateway-v1");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("does not cache health or API reads and preserves Backend status codes", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response('{"ok":true}', { status: 401 });
  try {
    const response = await gateway.fetch(apiRequest("/healthz"), { APP_ORIGIN, EDGE_PROXY_SHARED_TOKEN: "a".repeat(64) });
    assert.equal(response.status, 401);
    assert.equal(response.headers.get("cache-control"), "no-store");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("rejects a non-InstaBox or non-HTTPS origin before making an upstream request", async () => {
  const originalFetch = globalThis.fetch;
  let called = false;
  globalThis.fetch = async () => {
    called = true;
    throw new Error("must not be called");
  };
  try {
    for (const origin of [
      "https://attacker.example",
      "https://8080-agq4me2xgrgx5ge52ts437tqea.instabox.tech",
      "https://9999-agq4me2xgrgx5ge52ts437tqea.instabox.tech",
    ]) {
      const response = await gateway.fetch(apiRequest("/healthz"), {
        APP_ORIGIN: origin,
        EDGE_PROXY_SHARED_TOKEN: "a".repeat(64),
      });
      assert.equal(response.status, 503);
      assert.equal((await response.json()).error.code, "api_origin_misconfigured");
    }
    assert.equal(called, false);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("rejects requests for another hostname", async () => {
  const response = await gateway.fetch(new Request("https://evil.example/api"), { APP_ORIGIN, EDGE_PROXY_SHARED_TOKEN: "a".repeat(64) });
  assert.equal(response.status, 421);
});

test("marks upstream 503 responses for safe client recovery and normalizes gateway HTML", async () => {
  const originalFetch = globalThis.fetch;
  try {
    globalThis.fetch = async () => new Response("<html>machine waking</html>", {
      status: 503,
      headers: { "content-type": "text/html" },
    });
    const gatewayResponse = await gateway.fetch(apiRequest("/healthz"), {
      APP_ORIGIN,
      EDGE_PROXY_SHARED_TOKEN: "a".repeat(64),
    });
    assert.equal(gatewayResponse.status, 503);
    assert.equal(gatewayResponse.headers.get("x-testagram-recovery"), "attempted");
    assert.equal(gatewayResponse.headers.get("retry-after"), "2");
    assert.equal(gatewayResponse.headers.get("cache-control"), "no-store");
    assert.deepEqual(await gatewayResponse.json(), {
      error: {
        code: "api_origin_unavailable",
        message: "Testagram API is waking or temporarily unavailable.",
      },
    });

    globalThis.fetch = async () => new Response(
      JSON.stringify({ ok: false, error: { code: "BACKEND_TEMPORARY", message: "Retry later" } }),
      { status: 503, headers: { "content-type": "application/json", "retry-after": "9" } },
    );
    const backendResponse = await gateway.fetch(apiRequest("/api/feed/home"), {
      APP_ORIGIN,
      EDGE_PROXY_SHARED_TOKEN: "a".repeat(64),
    });
    assert.equal(backendResponse.status, 503);
    assert.equal(backendResponse.headers.get("x-testagram-recovery"), "attempted");
    assert.equal(backendResponse.headers.get("retry-after"), "9");
    assert.deepEqual(await backendResponse.json(), {
      ok: false,
      error: { code: "BACKEND_TEMPORARY", message: "Retry later" },
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("returns a bounded 503 without leaking upstream exception details", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    throw new Error("private origin detail");
  };
  try {
    const response = await gateway.fetch(apiRequest("/api/feed/home"), { APP_ORIGIN, EDGE_PROXY_SHARED_TOKEN: "a".repeat(64) });
    assert.equal(response.status, 503);
    assert.equal(response.headers.get("retry-after"), "2");
    assert.equal(response.headers.get("x-testagram-recovery"), "attempted");
    assert.equal((await response.text()).includes("private origin detail"), false);
  } finally {
    globalThis.fetch = originalFetch;
  }
});


test("fails closed when the shared edge token is absent or malformed", async () => {
  const originalFetch = globalThis.fetch;
  let called = false;
  globalThis.fetch = async () => {
    called = true;
    throw new Error("must not reach origin");
  };
  try {
    for (const token of [undefined, "short", "z".repeat(64)]) {
      const response = await gateway.fetch(apiRequest("/healthz"), {
        APP_ORIGIN,
        EDGE_PROXY_SHARED_TOKEN: token,
      });
      assert.equal(response.status, 503);
      assert.equal((await response.json()).error.code, "api_origin_misconfigured");
    }
    assert.equal(called, false);
  } finally {
    globalThis.fetch = originalFetch;
  }
});


test("forwards Cloudflare Email Sending Queue events to the pinned backend and acknowledges only HTTP 202", async () => {
  const originalFetch = globalThis.fetch;
  const calls = [];
  const actions = [];
  globalThis.fetch = async (input, init) => {
    calls.push({ input: String(input), init });
    return new Response("accepted", { status: 202 });
  };
  try {
    const body = {
      type: "cf.email.sending.message.delivered",
      source: { type: "email.sending", domain: "testagram.site" },
      payload: { eventId: "event-1", messageId: "message-1", recipient: "person@example.net" },
    };
    const message = {
      body,
      ack: () => actions.push("ack"),
      retry: () => actions.push("retry"),
    };
    await gateway.queue({ messages: [message] }, {
      APP_ORIGIN,
      EDGE_PROXY_SHARED_TOKEN: "a".repeat(64),
      TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN: "b".repeat(64),
    });
    assert.equal(calls.length, 1);
    assert.equal(calls[0].input, APP_ORIGIN + "/internal/mail/events");
    assert.equal(calls[0].init.method, "POST");
    assert.equal(calls[0].init.headers.Authorization, "Bearer " + "b".repeat(64));
    assert.equal(calls[0].init.headers["X-Testagram-Edge-Token"], "a".repeat(64));
    assert.deepEqual(JSON.parse(calls[0].init.body), body);
    assert.deepEqual(actions, ["ack"]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("retries Queue events on non-202 responses, transport errors and missing secrets", async () => {
  const originalFetch = globalThis.fetch;
  const actions = [];
  const makeMessage = (name) => ({
    body: { type: "cf.email.sending.message.failed", payload: { eventId: name } },
    ack: () => actions.push(name + ":ack"),
    retry: () => actions.push(name + ":retry"),
  });
  try {
    globalThis.fetch = async () => new Response("unavailable", { status: 503 });
    await gateway.queue({ messages: [makeMessage("server-error")] }, {
      APP_ORIGIN,
      EDGE_PROXY_SHARED_TOKEN: "a".repeat(64),
      TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN: "b".repeat(64),
    });

    globalThis.fetch = async () => { throw new Error("private origin detail"); };
    await gateway.queue({ messages: [makeMessage("network-error")] }, {
      APP_ORIGIN,
      EDGE_PROXY_SHARED_TOKEN: "a".repeat(64),
      TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN: "b".repeat(64),
    });

    await gateway.queue({ messages: [makeMessage("missing-secret")] }, {
      APP_ORIGIN,
      EDGE_PROXY_SHARED_TOKEN: "a".repeat(64),
    });
    assert.deepEqual(actions, [
      "server-error:retry",
      "network-error:retry",
      "missing-secret:retry",
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
