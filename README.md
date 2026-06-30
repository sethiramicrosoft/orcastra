# Orcastra

> The self-hosted PaaS where the AI isn't bolted on — it's the product.

Deploy apps, databases, and services on your own servers. When something breaks, Orcastra tells you why and offers to fix it.

## What it is

Orcastra is a free, open-source, self-hosted platform you run on your own Linux server. Point it at your server, connect a Git repo, and your app is live with HTTPS — without touching nginx, certbot, or Docker manually.

When a deploy fails, Orcastra tells you why.

## Status

🚧 **Pre-alpha — actively building**

## Stack

- **Backend:** Go
- **Frontend:** React + Vite
- **Database:** PostgreSQL 16
- **Reverse proxy:** Caddy (automatic SSL)
- **Distribution:** Docker Compose

## Quick start (local)

```bash
cp .env.example .env
docker compose -f deploy/docker-compose.yml up -d --build
```

`ENCRYPTION_KEY_B64` is recommended for stable encryption keys across restarts.
If omitted, Orcastra derives a key from `JWT_SECRET` for local/dev compatibility.
`HEALTH_CHECK_INTERVAL` controls background deployment health checks (default `30s`).

Then open:

- `http://localhost:3000/` (frontend)
- `http://localhost:3000/healthz` (API health)

## Frontend (fun colorful UI shell)

```bash
cd web
npm install
npm run dev
```

Open `http://localhost:5173`.

If backend is not on localhost:3000:

```bash
cd web
VITE_API_BASE=http://your-api-host:3000 npm run dev
```

## Why Orcastra over Coolify / Dokploy?

Those tools deploy your app. Orcastra deploys your app **and understands it**.

- Every deploy failure gets a plain-English diagnosis and a suggested fix
- Deploy history is indexed so the AI can spot patterns ("this fails every time after a DB migration")
- One-click "open a fix PR" — not just a suggestion, an actual pull request
- Proactive health monitoring — AI alerts you before users notice
- Natural-language onboarding: paste your `docker run` command, get a configured service

Bring your own LLM: OpenAI, Anthropic, Gemini, OpenRouter (200+ models), Groq, Mistral, Ollama (local/free), or any OpenAI-compatible endpoint.

## Features (v1 scope)

- [ ] SSH server onboarding (keypair-based, Orcastra generates the keypair)
- [ ] Docker container orchestration (apps, databases, workers)
- [x] Caddy reverse proxy route automation via Caddy Admin API
- [ ] Git webhook auto-deploy (GitHub, GitLab, Bitbucket)
- [x] Real-time deploy logs (SSE, structured + stored for AI)
- [x] Service secret management (versioned env vars per service)
- [x] AES-GCM encryption at rest for service secrets and AI provider API keys
- [ ] Resource monitoring (CPU, RAM, disk)
- [x] Multi-user auth (teams, roles, audit log)
- [x] **AI deploy failure analysis** — diagnosis + suggested fix on every failure
- [x] **AI fix PR** — one click to open a draft pull request with generated fix context
- [x] **AI health monitoring** — periodic running-container checks with alert/audit events

Remote SSH deploys require host fingerprint pinning; insecure host-key fallback is disabled.

`GITHUB_TOKEN` is required for fix PR creation. The token must have repository write permissions.

## API snippets

Set a service secret (creates a new immutable version each time):

```bash
curl -X POST http://localhost:3000/api/v1/services/<service-id>/secrets \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"key":"DATABASE_URL","value":"postgres://..."}'
```

List current secret keys for a service:

```bash
curl http://localhost:3000/api/v1/services/<service-id>/secrets \
  -H "Authorization: Bearer <token>"
```

Upsert a domain route for a service (writes DB + configures Caddy):

```bash
curl -X POST http://localhost:3000/api/v1/services/<service-id>/domains \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"fqdn":"app.example.com","sslEnabled":true}'
```

Open a draft fix PR from a failed deployment:

```bash
curl -X POST http://localhost:3000/api/v1/deployments/<deployment-id>/fix-pr \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{}'
```

## Architecture principles

- `HostDriver` interface — SSH is driver #1, local Docker socket is driver #2. Kubernetes is a future driver.
- PostgreSQL only — no SQLite. Concurrent webhook + log + reconciler writes require real transaction isolation.
- Caddy is the one reverse proxy. Not Traefik. Not "either".
- Reverse proxy is a sibling process, never a child of Orcastra. Upgrading Orcastra does not restart your proxy.
- Audit log from day one. Every state-changing action is recorded.
- Envelope encryption for all secrets. Every ciphertext column has a `_kid` sibling for key rotation.

## License

Apache 2.0 — free to self-host, fork, and build on.
