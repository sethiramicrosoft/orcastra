# Orcastra

> Self-hosted VPS management platform — deploy apps, databases, and services on your own servers, with AI-powered deploy failure analysis.

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

## Quick start

```bash
curl -fsSL https://orcastra.dev/install.sh | sh
```

Then open `http://your-server-ip:3000`.

## Features (v1 scope)

- [ ] SSH server onboarding (keypair-based, never paste your private key)
- [ ] Docker container orchestration (deploy apps, databases, workers)
- [ ] Caddy reverse proxy with automatic Let's Encrypt SSL
- [ ] Git webhook auto-deploy (GitHub, GitLab, Bitbucket)
- [ ] Real-time deploy logs (SSE)
- [ ] Resource monitoring (CPU, RAM, disk)
- [ ] Multi-user auth (teams, roles)
- [ ] **AI deploy failure analysis** — when a deploy fails, get a one-sentence diagnosis and suggested fix (bring your own OpenAI/Anthropic API key)

## Architecture principles

- `HostDriver` interface — SSH is driver #1, local Docker socket is driver #2. Kubernetes is a future driver.
- PostgreSQL only — no SQLite. Concurrent webhook + log + reconciler writes require real transaction isolation.
- Caddy is the one reverse proxy. Not Traefik. Not "either".
- Reverse proxy is a sibling process, never a child of Orcastra. Upgrading Orcastra does not restart your proxy.
- Audit log from day one. Every state-changing action is recorded.
- Envelope encryption for all secrets. Every ciphertext column has a `_kid` sibling for key rotation.

## License

Apache 2.0 — free to self-host, fork, and build on.
