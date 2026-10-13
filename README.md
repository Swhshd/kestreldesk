# KestrelDesk

> This file has been modified for the KestrelDesk project.

**Self-hosted AI customer support and realtime collaboration.**

KestrelDesk is a Go-based support platform with a customer web channel, an employee workspace, AI-assisted answers, and realtime conversation handling.

- Customer support entry and embeddable Web channel
- Employee conversation workspace, roles, permissions, and session revocation
- Knowledge-base retrieval and AI-assisted answers, with human handoff
- Customer identity isolation and guarded WebSocket lifecycles
- Docker Compose deployment with MySQL and Qdrant

[中文说明](README_ZH.md) · [Development](docs/DEVELOPMENT.md) · [Security](SECURITY.md)

## Screenshots

The employee workspace capture uses synthetic local data.

![Employee workspace](screenshots/kestreldesk/employee-workspace.jpg)

## Architecture

The Go service serves the web application and API. MySQL stores relational application data; Qdrant stores vectors for retrieval. Operators configure model providers, channels, and email delivery for their own deployment. Docker Compose starts the application and its MySQL/Qdrant dependencies.

## Docker quick start

Prerequisites: Docker Engine with the Compose v2 plugin, and enough memory and disk space to build the web and Go images.

From the repository root:

1. Copy `.env.example` to `.env`.
2. Set unique, randomly generated values for `MYSQL_PASSWORD`, `MYSQL_ROOT_PASSWORD`, and `CUSTOMER_SESSION_SECRET`. Use long hexadecimal values for the database passwords so they can be safely embedded in the MySQL connection string.
3. Before the first database initialization, set `BOOTSTRAP_ADMIN_PASSWORD` to a unique administrator password.
4. Validate the Compose configuration, then build and start the services:

```powershell
docker compose config -q
docker compose up --build -d
docker compose ps
```

Open [http://localhost:8083](http://localhost:8083). The initial administrator account is `admin`; its password is the value supplied through `BOOTSTRAP_ADMIN_PASSWORD`. The service refuses a fresh initialization when this value is empty. After the initial administrator has been created, remove that value from `.env` and recreate only the app service:

```powershell
docker compose up -d --no-deps --force-recreate kestreldesk
```

The default Compose file publishes the application on host port 8083. Qdrant's REST/gRPC ports (6333/6334) are bound to `127.0.0.1` for local access. Self-hosted Qdrant has no authentication or TLS by default; do not expose these ports to untrusted networks. For a networked deployment, use a private network binding, enable Qdrant API-key authentication and TLS, and restrict application access behind HTTPS. Configure the real browser origins and store `.env` and database backups as secrets.

## Configure AI, knowledge, and Web support

After signing in:

1. Add chat and embedding provider configurations in **Dashboard → AI Configs**. Supply your own provider credentials; these are deployment data.
2. Add a knowledge base and its content in **Dashboard → Knowledge**, then connect it to an Agent in **Dashboard → AI Agents**.
3. Create a **Web** channel in **Dashboard → Channels** and configure the origins allowed to embed it.
4. Use the channel's generated widget or customer entry link. The support center is available at `/support`.

For cross-origin embeds, set `server.cors.allowedOrigins` in the mounted configuration to the exact HTTPS origins that host the widget. Do not use a wildcard for authenticated or customer data surfaces.

## Password recovery and email

A local operator can reset the administrator password without email. In Docker, run this command from the repository root and enter the new password at the hidden prompts:

```powershell
docker compose exec kestreldesk /app/kestreldesk admin reset-password --username admin --config /app/config/config.yaml
```

For a locally built binary, use `kestreldesk admin reset-password --username admin --config config/config.yaml`. The command asks for the password twice without echoing it and revokes active sessions for that account.

For customer or employee email recovery, set the optional SMTP values in the project `.env` or the mounted private config file. Compose forwards `EMAIL_ENABLED`, `EMAIL_PROVIDER`, `EMAIL_FROM`, `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_SECURITY`, `SMTP_USE_TLS`, and `SMTP_TIMEOUT_SECONDS` to the application. Set `EMAIL_PROVIDER=smtp` and `SMTP_SECURITY` to `starttls` or `tls`, according to your provider. `SMTP_USE_TLS` is retained as a legacy direct-TLS setting when `SMTP_SECURITY` is unset; new configurations should use `SMTP_SECURITY`. `SMTP_TIMEOUT_SECONDS` defaults to 20 seconds and is capped at 30 seconds. Never commit the populated `.env` or config file; `.env` files are excluded from the Docker build context.

## Development and verification

See [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for reproducible frontend, Go, and Docker commands. The project uses the lockfiles in `web/` and `flowgram-editor/`; install with frozen lockfiles. Build the frontend export before running Go checks that embed it.

## Known limitations

- Native Windows LanceDB development requires CGO, a compatible compiler, and matching native artifacts; that environment is not established as a supported path.
- Windows race-enabled tests are environment-blocked; no Windows race pass is claimed.
- The broader internal/services race run previously timed out; it is not a passing result.
- WxWork callback timing remains a known issue.
- Ticket concurrent-write semantics have not been defined as a product contract.
- Attachment direct-URL authorization and the end-to-end human handoff experience remain incomplete.

## License

KestrelDesk is distributed under the Apache License 2.0. See [LICENSE](LICENSE) for the license terms and applicable notices.
