import test from "node:test";
import assert from "node:assert/strict";
import worker from "./index.mjs";

test("redirects apex root to canonical www with permanent status", async () => {
  const request = new Request("https://testagram.site/");
  const response = await worker.fetch(request);
  assert.equal(response.status, 308);
  assert.equal(response.headers.get("location"), "https://www.testagram.site/");
});

test("preserves path and query while forcing HTTPS", async () => {
  const request = new Request("http://testagram.site/account/reset?token=abc&next=%2Fhome");
  const response = await worker.fetch(request);
  assert.equal(response.status, 308);
  assert.equal(
    response.headers.get("location"),
    "https://www.testagram.site/account/reset?token=abc&next=%2Fhome",
  );
});
