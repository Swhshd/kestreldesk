# Security

> This file has been modified for the KestrelDesk project.

KestrelDesk handles customer conversations, employee accounts, model-provider credentials, and customer-provided content. Treat the application, its configuration, and its backing data as sensitive.

## Deployment

- The default Compose file publishes the app on host port 8083. Restrict it to trusted networks or place it behind a maintained HTTPS reverse proxy before internet exposure.
- Qdrant's REST/gRPC host ports (6333/6334) bind to `127.0.0.1` for local access. Self-hosted Qdrant has no authentication or TLS by default; do not expose these ports to untrusted networks. If remote access is required, use a private network binding and configure Qdrant API-key authentication and TLS.
- Set `server.cors.allowedOrigins` to the exact browser origins that need access. Avoid wildcard origins for customer or authenticated surfaces.
- Use unique, randomly generated database and customer-session values. Keep populated `.env`, private config files, and database/vector-store backups out of source control and access logs.
- Root and nested `.env` files are excluded from Docker build contexts. Compose forwards only explicitly listed variables to the application container.
- Configure TLS for SMTP according to the provider's current requirements.
- Protect MySQL, Qdrant, uploaded files, and backups with deployment-appropriate access controls and retention.

## Account recovery

For a local operator reset, run the documented `admin reset-password` command from [README.md](README.md). It reads the new password through hidden terminal input and revokes active sessions. Do not pass passwords as command-line arguments or include them in issue reports.

Customer/employee email recovery requires outbound mail configuration. A working SMTP connection is required for email delivery; the local operator reset remains available if mail is not configured.

## Reporting

Do not publish credentials, personal data, reset links, session cookies, or unredacted diagnostic bundles in public issues. Share a minimal, redacted reproduction with the project maintainers through a private channel.

## Known limitations

Windows native LanceDB development and Windows race execution are environment-blocked as described in [Development](docs/DEVELOPMENT.md). The broader internal/services race run previously timed out. WxWork callback timing remains a known issue, and ticket concurrent-write semantics are not yet a product contract.
