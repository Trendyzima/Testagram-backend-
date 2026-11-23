const API_HOST = "api.testagram.site";
const EXPECTED_ORIGIN_HOST = "8443-agq4me2xgrgx5ge52ts437tqea.instabox.tech";
const HOP_BY_HOP_HEADERS = [
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
];

function jsonError(status, code, message, extraHeaders = {}) {
  return new Response(JSON.stringify({ error: { code, message } }), {
    status,
    headers: {
      "content-type": "application/json; charset=utf-8",
      "cache-control": "no-store",
      "x-content-type-options": "nosniff",
      ...extraHeaders,
    },
  });
}

function resolveOrigin(value) {
  let origin;
  try {
    origin = new URL(value);
  } catch {
    return null;
  }

  if (
    origin.protocol !== "https:" ||
    origin.hostname !== EXPECTED_ORIGIN_HOST ||
    origin.username ||
    origin.password ||
    origin.pathname !== "/" ||
    origin.search ||
    origin.hash
  ) {
    return null;
  }

  return origin;
}

function trustedRequestHeaders(request, edgeToken) {
  const headers = new Headers(request.headers);
  const connectionTokens = (headers.get("connection") || "")
    .split(",")
    .map((value) => value.trim().toLowerCase())
    .filter(Boolean);

  for (const name of [...HOP_BY_HOP_HEADERS, ...connectionTokens]) {
    headers.delete(name);
  }

  // Never trust client-supplied forwarding metadata. Cloudflare overwrites
  // CF-Connecting-IP at its edge; use that value as the sole client address.
  for (const name of [
    "host",
    "forwarded",
    "x-forwarded-for",
    "x-forwarded-host",
    "x-forwarded-proto",
    "x-real-ip",
  ]) {
    headers.delete(name);
  }

  const clientIp = request.headers.get("cf-connecting-ip");
  if (clientIp) {
    headers.set("x-forwarded-for", clientIp);
    headers.set("x-real-ip", clientIp);
  }

  headers.set("x-forwarded-host", API_HOST);
  headers.set("x-forwarded-proto", "https");
  headers.set("x-request-id", request.headers.get("cf-ray") || crypto.randomUUID());
  headers.set("x-testagram-edge", "cloudflare-api-gateway-v1");
  headers.set("x-testagram-edge-token", edgeToken);
  return headers;
}

export default {
  async fetch(request, env) {
    const incoming = new URL(request.url);

    if (incoming.hostname !== API_HOST) {
      return jsonError(421, "misdirected_request", "This API gateway only serves api.testagram.site.");
    }

    if (incoming.protocol !== "https:") {
      return Response.redirect(new URL(incoming.pathname + incoming.search, "https://" + API_HOST), 308);
    }

    const origin = resolveOrigin(env.APP_ORIGIN);
    const edgeToken = env.EDGE_PROXY_SHARED_TOKEN;
    if (!origin || !/^[a-f0-9]{64}$/i.test(edgeToken || "")) {
      return jsonError(503, "api_origin_misconfigured", "The Testagram API origin is not configured safely.");
    }

    // Preserve the exact path and query while changing only the upstream
    // authority. No request method is retried here: mutation outcomes must
    // remain governed by Backend idempotency/reconciliation contracts.
    const upstreamUrl = new URL(incoming.pathname + incoming.search, origin);
    const init = {
      method: request.method,
      headers: trustedRequestHeaders(request, edgeToken),
      redirect: "manual",
      cf: { cacheTtl: 0, cacheEverything: false },
    };

    if (request.method !== "GET" && request.method !== "HEAD") {
      init.body = request.body;
      // Required by Node's Fetch implementation in the unit-test runtime;
      // Workers accepts the same streaming request body semantics.
      init.duplex = "half";
    }

    try {
      const upstreamResponse = await fetch(new Request(upstreamUrl, init));
      const responseHeaders = new Headers(upstreamResponse.headers);
      responseHeaders.set("cache-control", "no-store");
      responseHeaders.set("x-content-type-options", "nosniff");
      responseHeaders.set("x-testagram-edge", "cloudflare-api-gateway-v1");
      responseHeaders.delete("server");

      // A published InstaBox port can answer 503 while an idle-paused
      // machine is waking. Mark that response so clients can apply only their
      // endpoint-specific safe retry policy. Normalize non-JSON gateway pages
      // to the same machine-readable envelope used for fetch exceptions.
      if (upstreamResponse.status === 503) {
        responseHeaders.set("x-testagram-recovery", "attempted");
        if (!responseHeaders.has("retry-after")) {
          responseHeaders.set("retry-after", "2");
        }
        const contentType = responseHeaders.get("content-type") || "";
        if (!contentType.toLowerCase().includes("json")) {
          return jsonError(503, "api_origin_unavailable", "Testagram API is waking or temporarily unavailable.", {
            "retry-after": responseHeaders.get("retry-after") || "2",
            "x-testagram-recovery": "attempted",
            "x-testagram-edge": "cloudflare-api-gateway-v1",
          });
        }
      }

      return new Response(upstreamResponse.body, {
        status: upstreamResponse.status,
        statusText: upstreamResponse.statusText,
        headers: responseHeaders,
      });
    } catch {
      // The origin may be idle-paused or temporarily unavailable. The caller
      // may retry safe reads; writes must reuse their idempotency key and
      // reconcile state before any retry.
      return jsonError(503, "api_origin_unavailable", "Testagram API is waking or temporarily unavailable.", {
        "retry-after": "2",
        "x-testagram-recovery": "attempted",
      });
    }
  },,

  async queue(batch, env) {
    const origin = resolveOrigin(env.APP_ORIGIN);
    const edgeToken = env.EDGE_PROXY_SHARED_TOKEN;
    const eventsToken = env.TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN;
    if (
      !origin ||
      !/^[a-f0-9]{64}$/i.test(edgeToken || "") ||
      !/^[a-f0-9]{64}$/i.test(eventsToken || "")
    ) {
      for (const message of batch.messages) message.retry();
      return;
    }

    const endpoint = new URL("/internal/mail/events", origin);
    for (const message of batch.messages) {
      try {
        const body = typeof message.body === "string"
          ? message.body
          : JSON.stringify(message.body);
        const response = await fetch(endpoint, {
          method: "POST",
          headers: {
            "Authorization": `Bearer ${eventsToken}`,
            "Content-Type": "application/json",
            "X-Testagram-Edge-Token": edgeToken,
          },
          body,
          redirect: "manual",
        });
        if (response.status === 202) message.ack();
        else message.retry();
      } catch {
        // Preserve Queue's at-least-once delivery contract. The backend's
        // provider_event_id uniqueness makes retries safe.
        message.retry();
      }
    }
  },
};
