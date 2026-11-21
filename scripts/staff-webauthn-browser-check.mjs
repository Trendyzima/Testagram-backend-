import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, resolve, sep } from "node:path";
import { chromium } from "playwright";

const webRoot = resolve("admin-console/web");
const mimeTypes = new Map([
  [".html", "text/html; charset=utf-8"],
  [".js", "text/javascript; charset=utf-8"],
  [".css", "text/css; charset=utf-8"],
]);

const server = createServer(async (request, response) => {
  const pathname = new URL(request.url || "/", "http://localhost").pathname;
  const relative = pathname === "/" ? "index.html" : decodeURIComponent(pathname.slice(1));
  const file = resolve(webRoot, relative);
  if (!file.startsWith(webRoot + sep)) {
    response.writeHead(403).end();
    return;
  }
  try {
    const body = await readFile(file);
    response.writeHead(200, { "content-type": mimeTypes.get(extname(file)) || "application/octet-stream", "cache-control": "no-store" });
    response.end(body);
  } catch {
    response.writeHead(404).end();
  }
});

await new Promise((resolveListen, reject) => {
  server.once("error", reject);
  server.listen(0, "::", resolveListen);
});
const address = server.address();
assert.ok(address && typeof address === "object");
const origin = `http://localhost:${address.port}`;
const registrationChallenge = randomBytes(32).toString("base64url");
const assertionChallenge = randomBytes(32).toString("base64url");
let registeredCredentialId = "";
let registrationVerified = false;
let assertionVerified = false;
let ceremonyFailure = null;

function decodeClientData(value) {
  return JSON.parse(Buffer.from(value, "base64url").toString("utf8"));
}

let browser;
try {
  browser = await chromium.launch({ headless: true, args: ["--no-sandbox"] });
  const context = await browser.newContext();
  const page = await context.newPage();
  const cdp = await context.newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  const { authenticatorId } = await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: {
      protocol: "ctap2",
      transport: "usb",
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  });

  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    let data = {};

    try {
      if (pathname === "/api/session") {
        data = { authenticated: true, staff: true, role: "owner", email: "owner@testagram.site" };
      } else if (pathname === "/api/overview") {
        data = { api_health: "healthy", checked_at: new Date().toISOString(), metrics: {} };
      } else if (pathname === "/api/admin/sensitive-actions" ||
                 pathname === "/api/admin/identity/review-queue" ||
                 pathname === "/api/admin/audit" ||
                 pathname === "/api/admin/staff") {
        data = { items: [] };
      } else if (pathname === "/api/admin/webauthn/readiness") {
        data = {
          ready: true,
          rpid_configured: true,
          origins_configured: true,
          database: true,
          schema: true,
          privileges: true,
          signing_key: true,
          credential_encryption: true,
          backend_api: true,
          enrollment_enabled: true,
        };
      } else if (pathname === "/api/admin/webauthn/registration/begin") {
        data = {
          challenge: registrationChallenge,
          publicKey: {
            challenge: registrationChallenge,
            rp: { id: "localhost", name: "Testagram" },
            user: {
              id: Buffer.from("testagram-owner-staff").toString("base64url"),
              name: "owner@testagram.site",
              displayName: "Testagram Owner",
            },
            pubKeyCredParams: [{ type: "public-key", alg: -7 }],
            timeout: 60000,
            attestation: "none",
            authenticatorSelection: { residentKey: "discouraged", userVerification: "required" },
            excludeCredentials: [],
          },
        };
      } else if (pathname === "/api/admin/webauthn/registration/finish") {
        const body = await request.postDataJSON();
        const credential = body.response;
        const clientData = decodeClientData(credential.response.clientDataJSON);
        assert.equal(body.challenge, registrationChallenge);
        assert.equal(clientData.type, "webauthn.create");
        assert.equal(clientData.challenge, registrationChallenge);
        assert.equal(clientData.origin, origin);
        assert.equal(credential.type, "public-key");
        assert.ok(credential.rawId);
        assert.ok(credential.response.attestationObject);
        registeredCredentialId = credential.rawId;
        registrationVerified = true;
        data = { ok: true };
      } else if (pathname === "/api/admin/webauthn/assertion/begin") {
        assert.ok(registeredCredentialId, "registration must complete before assertion begins");
        data = {
          challenge: assertionChallenge,
          publicKey: {
            challenge: assertionChallenge,
            rpId: "localhost",
            allowCredentials: [{ type: "public-key", id: registeredCredentialId }],
            timeout: 60000,
            userVerification: "required",
          },
        };
      } else if (pathname === "/api/admin/webauthn/assertion/finish") {
        const body = await request.postDataJSON();
        const credential = body.response;
        const clientData = decodeClientData(credential.response.clientDataJSON);
        assert.equal(body.challenge, assertionChallenge);
        assert.equal(clientData.type, "webauthn.get");
        assert.equal(clientData.challenge, assertionChallenge);
        assert.equal(clientData.origin, origin);
        assert.equal(credential.rawId, registeredCredentialId);
        assert.ok(credential.response.authenticatorData);
        assert.ok(credential.response.signature);
        assertionVerified = true;
        data = { expires_at: Math.floor(Date.now() / 1000) + 300 };
      }

      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(data) });
    } catch (error) {
      ceremonyFailure = error;
      await route.fulfill({
        status: 400,
        contentType: "application/json",
        body: JSON.stringify({ error: error instanceof Error ? error.message : String(error) }),
      });
    }
  });

  await page.goto(origin, { waitUntil: "domcontentloaded" });
  await page.waitForFunction(() => {
    const dashboard = document.querySelector("#dashboard-screen");
    return dashboard && !dashboard.classList.contains("hidden");
  });
  await page.waitForFunction(() => document.querySelector("#webauthn-readiness")?.textContent.includes("All configured WebAuthn runtime prerequisites"));

  await page.locator("#webauthn-enroll").click();
  await page.waitForFunction(() => document.querySelector("#notice")?.textContent.includes("Staff authenticator enrolled successfully."));
  assert.equal(registrationVerified, true, "the browser must send a complete registration attestation");
  assert.ok(registeredCredentialId, "the browser must return a credential ID");

  await page.locator("#stepup-form button[type=submit]").click();
  await page.waitForFunction(() => document.querySelector("#notice")?.textContent.includes("Phishing-resistant WebAuthn verification succeeded."));
  assert.equal(assertionVerified, true, "the browser must send a complete signed assertion");
  assert.match(await page.locator("#stepup-state").textContent(), /Backend-issued MFA elevation expires at/);

  const credentials = await cdp.send("WebAuthn.getCredentials", { authenticatorId });
  assert.equal(credentials.credentials.length, 1, "the virtual authenticator must retain the registered credential");
  console.log("Admin Console browser WebAuthn ceremonies passed: real Chromium registration attestation, credential-bound assertion, RP/origin/challenge validation and MFA elevation UI.");
} catch (error) {
  if (ceremonyFailure) throw new AggregateError([error, ceremonyFailure], "Browser WebAuthn ceremony failed");
  throw error;
} finally {
  if (browser) await browser.close();
  await new Promise((resolveClose) => server.close(resolveClose));
}
