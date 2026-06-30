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

Then open `http://localhost:3000/healthz`.

## Frontend (fun colorful UI shell)

```bash
cd web
npm install
npm run dev
```

Open `http://localhost:5173`.

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
- [ ] Caddy reverse proxy with automatic Let's Encrypt SSL
- [ ] Git webhook auto-deploy (GitHub, GitLab, Bitbucket)
- [ ] Real-time deploy logs (SSE, structured + stored for AI)
- [ ] Resource monitoring (CPU, RAM, disk)
- [ ] Multi-user auth (teams, roles, audit log)
- [ ] **AI deploy failure analysis** — diagnosis + suggested fix on every failure
- [ ] **AI fix PR** — one click to open a pull request with the suggested change
- [ ] **AI health monitoring** — plain-English alerts, not just graphs

## Architecture principles

- `HostDriver` interface — SSH is driver #1, local Docker socket is driver #2. Kubernetes is a future driver.
- PostgreSQL only — no SQLite. Concurrent webhook + log + reconciler writes require real transaction isolation.
- Caddy is the one reverse proxy. Not Traefik. Not "either".
- Reverse proxy is a sibling process, never a child of Orcastra. Upgrading Orcastra does not restart your proxy.
- Audit log from day one. Every state-changing action is recorded.
- Envelope encryption for all secrets. Every ciphertext column has a `_kid` sibling for key rotation.

## License

Apache 2.0 — free to self-host, fork, and build on.
