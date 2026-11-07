# PB-Recruitment Monitoring Stack

Production-grade observability stack featuring Prometheus, Loki, Promtail, Grafana, Node Exporter, and Uptime Kuma.

## Services & Ports

| Service | Host Port | Purpose |
| :--- | :--- | :--- |
| **Grafana** | `http://localhost:3000` | Unified Dashboards & Log Exploration (User: `admin`, Pass: `change_this_strong_password`) |
| **Uptime Kuma** | `http://localhost:3001` | External Synthetic Health Probes & Discord Alerts |
| **Prometheus** | `http://localhost:9090` | TSDB scraping `/metrics` from backend and host |
| **Loki** | `http://localhost:3100` | Log Aggregation Engine |
| **Promtail** | `http://localhost:9080` | Docker stdout tailer |
| **node_exporter** | `http://localhost:9100` | Host CPU, RAM, and Disk metrics |

## Quick Start

```bash
cd monitoring

# Start all observability services in the background
docker compose up -d

# Check status
docker compose ps

# View service logs
docker compose logs -f
```

## Stopping the Stack

```bash
docker compose down
```

*(Note: Data is preserved in Docker volumes `prometheus_data`, `loki_data`, `grafana_data`, and `uptime_kuma_data`)*

## Grafana Dashboards

Grafana automatically provisions Prometheus and Loki datasources on startup.

### Recommended Community Dashboards to Import:
1. **Node Exporter Full** (Dashboard ID: `1860`): Host CPU, RAM, Disk I/O, Network.
2. **Go Processes** (Dashboard ID: `10826`): Go runtime stats (Goroutines, GC pauses, Heap alloc).

### Useful PromQL Queries for PB-Recruitment:
- **Throughput by Route**:
  ```promql
  sum(rate(http_requests_total[1m])) by (route)
  ```
- **5xx Error Rate %**:
  ```promql
  (sum(rate(http_requests_total{status=~"5.."}[1m])) / sum(rate(http_requests_total[1m]))) * 100
  ```
- **In-Flight Judge0 Executions Queue**:
  ```promql
  pb_judge_in_flight_submissions
  ```
- **Judge0 95th Percentile Latency**:
  ```promql
  histogram_quantile(0.95, sum(rate(pb_judge0_roundtrip_duration_seconds_bucket[5m])) by (le))
  ```
- **Postgres DB Connections In-Use**:
  ```promql
  go_sql_db_connections_in_use{db_name="pb_db"}
  ```

## Running Verification Load Test

```bash
# Requires k6 (https://k6.io/)
k6 run -e API_BASE_URL=http://localhost:8080 loadtest.js
```
