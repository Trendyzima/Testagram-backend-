# Testagram authenticated-activity watchdog

The durable watchdog queue belongs to the **Data Centre VPS control plane**, which also owns node registration and issues the node credentials used by the node agent. Backend emits coarse activity signals to that private control plane using a dedicated service credential. The browser and mobile clients never call watchdog endpoints.

## Trigger policy

- Successful login -> `algorithm`
- Authenticated feed open -> `algorithm`
- Successful post publish -> `data-centre`; public posts additionally wake `federation`
- Verified media upload and authorized private media download -> `cdn` and `data-centre`
- Federation publication/interaction -> `federation`

The Backend keeps canonical identity, session verification, profiles, posts, follows, blocks, privacy, moderation and media ownership. Activity impulses contain only an allowlisted event type and a random idempotency key; no user ID, email, IP address, session token or post content is sent.

## Deployment order and private configuration

Start the Backend self-hosted Compose stack first. It creates the shared internal `testagram-platform` and `testagram-ranker` Docker networks. The Data Centre stack consumes these as external networks; its installer checks that both already exist. Backend can start with ranking optional and falls back chronologically until the Algorithm service is running.

Backend environment:
- `VPS_WATCHDOG_INTERNAL_URL`: private Data Centre control-plane origin, e.g. `http://control-plane:8787` on a private service network
- `VPS_WATCHDOG_INTERNAL_TOKEN`: dedicated random service credential (generate with `openssl rand -hex 32` and set the identical value in the Backend and Data Centre environment files)

Data Centre control-plane environment:
- `VPS_WATCHDOG_INTERNAL_TOKEN`: same dedicated credential, distinct from `VPS_INTERNAL_TOKEN` and `VPS_NODE_BOOTSTRAP_TOKEN`

Node-agent environment:
- `VPS_ENABLE_WATCHDOG_ACTIVATION=false` by default
- `VPS_WATCHDOG_DATA_CENTRE_COMPOSE_FILE`
- `VPS_WATCHDOG_CDN_COMPOSE_FILE`

The Backend call is best-effort and time-bounded; failure to wake optional services never rejects a login, feed read, post, upload or media authorization request.

## Runtime and safety

Only a recent, enrolled node that advertises `watchdogActivation=true` can claim impulses using its node token. Claims are leased, concurrent-safe and retried with bounded exponential backoff. The node agent starts only compile-time allowlisted Compose services using preinstalled images. It cannot receive arbitrary commands, image names, Compose paths or environment values from events.

The agent never issues `docker compose down`, stop or prune. Backend, PostgreSQL, authentication, public ingress, the control plane and persistent data services remain available; public CDN and Federation services are start-only in this release. Do not enable activation until the Data Centre migration has been applied, the dedicated token is configured on both sides, and staging verifies the full enqueue/claim/activate/ack cycle.

## Social architecture

- X-inspired: high-throughput timelines, engagement signals, reposts, replies and ranking.
- Bluesky-inspired: composable feed-generation boundaries and portable event/identity interfaces; this does not imply an AT Protocol or Bluesky account integration.
- Mastodon-inspired: ActivityPub actor identity, signed federation, followers and remote delivery; public federation remains subject to explicit account consent and the Federation release gates.

Testagram remains the authority for local identity and product permissions. Remote platforms never become authentication providers for local accounts.


## Credential boundaries and runtime worker

Use two independent high-entropy secrets:

- `VPS_WATCHDOG_INGRESS_TOKEN`: mounted only in the Backend API and control-plane containers. It authorizes enqueue only.
- `VPS_WATCHDOG_TOKEN`: mounted only in the control-plane and the designated node-agent. It authorizes claim/ack only.

The API calls the private control-plane over the shared Docker network at `http://control-plane:8787` by default. Never route this internal endpoint through Cloudflare or expose it to Flutter/web clients.

The node-agent worker is disabled by default. Enable it only on the designated Linux activation VPS after the Backend migration and control-plane endpoints have been deployed. Configure `VPS_ENABLE_ACTIVITY_WATCHDOG=true`, `VPS_WATCHDOG_CONTROL_URL`, `VPS_WATCHDOG_TOKEN`, `VPS_WATCHDOG_DATA_CENTRE_COMPOSE_FILE` and `VPS_WATCHDOG_BACKEND_COMPOSE_FILE` in `/etc/testagram/node-agent.env`. The two Compose paths must be absolute paths to the already-deployed Data Centre and Backend Compose files.

The activation worker calls `POST /v1/watchdog/impulses/claim?limit=10` and `POST /v1/watchdog/impulses/ack`. The control plane retries failed/expired work with exponential backoff and stops after five attempts. The service lease is advisory only; this release never stops services on lease expiry. It does not run migrations, pull images, recreate running containers, accept arbitrary commands or alter public DNS/Cloudflare routes.
