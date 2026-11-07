# Testagram Edge Functions

This directory is the designated Backend-owned home for the former XClone edge-function source. The sources must be ported to the canonical Go API/worker runtime before production deployment. Existing Deno/Supabase Auth, PostgREST, RPC, and `SUPABASE_*` dependencies are legacy compatibility code and are not an approved runtime. Keep Appwrite identity and PostgreSQL persistence authoritative; keep ActivityPub protocol ownership in Federated Content. Do not add secrets to this repository.
