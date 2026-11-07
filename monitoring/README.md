# PB-Recruitment Cloud Observability Pipeline

Forwarder stack that scrapes local backend metrics and container logs, and securely streams them up to **Grafana Cloud** via HTTPS.

## Architecture

- **Prometheus**: Scrapes `pb-backend:8080/metrics` and forwards them to Grafana Cloud via `remote_write`.
- **Promtail**: Tails Docker container JSON logs and pushes them to Grafana Cloud Loki.
- **node_exporter**: Collects host CPU, memory, and disk stats.

All dashboards, alerts, and log searches are viewed at your hosted Grafana URL: `https://<your-org>.grafana.net`.

## Setup Instructions

1. Copy the example environment file:
   ```bash
   cp .env.example .env
   ```
2. In your Grafana Cloud account ([grafana.com](https://grafana.com)):
   - Under **Prometheus** → click **Send Metrics** and copy `URL`, `Username`, and generate an `API Token`.
   - Under **Loki** → click **Send Logs** and copy `URL` and `Username`.
   - Paste these values into `monitoring/.env`.

3. Start the forwarders:
   ```bash
   docker compose up -d
   ```

4. View your metrics and logs in Grafana Cloud: `https://<your-org>.grafana.net`!
