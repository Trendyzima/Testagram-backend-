export interface Env {
  TESTAGRAM_BOUNCE_INGRESS_URL: string;
  TESTAGRAM_BOUNCE_INGRESS_SECRET: string;
}

const MAX_MESSAGE_BYTES = 1 << 20;
const RECIPIENT = /^b\+[A-Za-z0-9_-]{32}@bounces\.testagram\.site$/;

function toHex(bytes: ArrayBuffer): string {
  return Array.from(new Uint8Array(bytes), (value) => value.toString(16).padStart(2, "0")).join("");
}

async function hmacHex(secret: string, payload: string): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return toHex(await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(payload)));
}

export default {
  async email(message: ForwardableEmailMessage, env: Env): Promise<void> {
    const rawRecipient = message.to.trim();
    const at = rawRecipient.lastIndexOf("@");
    const recipient = at > 0
      ? rawRecipient.slice(0, at) + "@" + rawRecipient.slice(at + 1).toLowerCase()
      : rawRecipient;
    if (!RECIPIENT.test(recipient)) {
      message.setReject("Unknown Testagram bounce address");
      return;
    }
    const secret = env.TESTAGRAM_BOUNCE_INGRESS_SECRET;
    if (!env.TESTAGRAM_BOUNCE_INGRESS_URL || typeof secret !== "string" || secret.length < 32) {
      throw new Error("Testagram bounce ingress is not configured");
    }
    if (message.rawSize <= 0 || message.rawSize > MAX_MESSAGE_BYTES) {
      message.setReject("Bounce report exceeds the accepted size limit");
      return;
    }

    // Preserve the original RFC 5322 message; Testagram performs all DSN parsing,
    // token correlation, idempotency, suppression, and delivery-state updates.
    const raw = await new Response(message.raw).arrayBuffer();
    if (raw.byteLength <= 0 || raw.byteLength > MAX_MESSAGE_BYTES) {
      message.setReject("Bounce report exceeds the accepted size limit");
      return;
    }
    const timestamp = Math.floor(Date.now() / 1000).toString();
    const bodyHash = toHex(await crypto.subtle.digest("SHA-256", raw));
    const payload = timestamp + "\n" + recipient + "\n" + bodyHash;
    const signature = await hmacHex(secret, payload);

    const response = await fetch(env.TESTAGRAM_BOUNCE_INGRESS_URL, {
      method: "POST",
      headers: {
        "Content-Type": "message/rfc822",
        "X-Testagram-Timestamp": timestamp,
        "X-Testagram-Recipient": recipient,
        "X-Testagram-Signature": "sha256=" + signature,
      },
      body: raw,
      redirect: "error",
    });
    if (!response.ok) {
      // Propagate handoff failures rather than acknowledging the message as processed.
      // Verify Cloudflare SMTP retry behavior with a controlled failure test before production.
      throw new Error("Testagram bounce ingress returned HTTP " + response.status);
    }
  },
} satisfies ExportedHandler<Env>;
