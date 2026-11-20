#!/usr/bin/env python3
"""Guard the Backend origin smoke and the post-origin gateway handoff."""

from pathlib import Path

caddy = Path("deploy/self-hosted/Caddyfile").read_text()
workflow = Path(".github/workflows/instabox-deploy.yml").read_text()

assert "@internal_observability path /metrics /readyz" in caddy
assert "respond @internal_observability 404" in caddy, (
    "Canonical Caddy must keep readiness and metrics private"
)
assert "handle /healthz" in caddy, (
    "Canonical Caddy must route the explicit Backend liveness endpoint"
)
assert "http://127.0.0.1:8082/readyz" in workflow, (
    "Deployment must verify Backend readiness from inside the container"
)

# The Backend deploy must verify the newly published origin directly. It must
# not wait for the public Worker gateway, which is deployed in the next stage.
assert "Verify public API liveness and private endpoint isolation" in workflow
assert '"$URL/healthz"' in workflow
assert "x-testagram-service" in workflow
assert '.ok == true and .service == "testagram-api"' in workflow
assert 'for path in /readyz /metrics; do' in workflow
assert "https://api.testagram.site" not in workflow
assert "CANONICAL_API_URL" not in workflow
assert 'PUBLIC_CDN_URL="https://media.testagram.site"' in workflow
assert 'upsert_env CDN_MEDIA_BASE_URL "$PUBLIC_CDN_URL"' in workflow
assert 'upsert_env MEDIA_PUBLIC_BASE_URL "$PUBLIC_CDN_URL/v1"' in workflow
assert 'check_env "$api_container" CDN_MEDIA_BASE_URL "$PUBLIC_CDN_URL"' in workflow
assert 'check_env "$api_container" MEDIA_PUBLIC_BASE_URL "$PUBLIC_CDN_URL/v1"' in workflow
assert 'curl -fsS "$CDN_ORIGIN/healthz"' in workflow
assert 'curl -fsS "$CDN_ORIGIN/readyz"' not in workflow

assert "Canonical hostname verification handoff" in workflow
assert "canonical Cloudflare routing workflow with API, Go CDN and apex origins" in workflow
assert "steps.publish_apex.outputs.apex_url" in workflow
assert "testagram-apex-edge" in workflow
assert "Apex Caddy ingress passed health, www redirect and WebFinger checks" in workflow

assert ":8443 {" in caddy
assert "handle /_testagram/edge-health {" in caddy
assert 'respond `{"ok":true,"service":"testagram-apex-edge"}` 200' in caddy
assert "reverse_proxy federation-api:8081" in caddy
assert "header_up Host testagram.site" in caddy
assert "header_up X-Forwarded-Host testagram.site" in caddy

print(
    "Backend origin smoke contract passed: Backend readiness is checked privately; "
    "public API/CDN listeners expose liveness only before Cloudflare host verification."
)
