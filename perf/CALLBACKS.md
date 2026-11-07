# Signed callbacks enabled — 6 November 2026

Callbacks were enabled at **18:19 UTC / 23:49 IST** on the existing recruitment
API image. Judge0 now sends a completion notification to the API over HTTPS.
Each callback URL carries an HMAC-SHA256 signature bound to its execution and
dispatch attempt. The API checks that signature and the execution/token state;
it rejects unsigned requests with HTTP 401. The signing secret stays in the
protected API runtime file and is never sent to a browser or committed here.
The signature authenticates the job-specific callback URL; TLS protects the
notification body in transit.

The normal 15-second recovery poller remains available if a callback is missed.
No submission/rate/admission limits or judging/score behavior changed.

## Verification

A real three-user Python/C++/Java smoke through the public API completed:

- Three saved submissions, all accepted with expected details.
- Twelve testcase jobs, all terminal; zero lost/unresolved submissions or score mismatches.
- Final-verdict p95 **12.27s**, versus **63.11s** in the earlier polling-only three-user smoke.
- Receipt p95 **2.22s**, read p95 **0.61s**, zero dropped iterations.
- Twelve distinct execution IDs with successful HTTP 200 callback delivery.
- Three initial callback attempts were canceled at approximately the configured
  five-second Judge0 timeout and returned HTTP 500. Each execution subsequently
  received HTTP 200 on retry. That is a 3/15 (20%) failed-attempt rate in this
  small burst; error categories and delivery counts are retained in the record. Eventual
  success does not make those failed attempts disappear. Callback latency and
  database contention still need investigation before capacity certification.
- No signature-shaped values found in API, ingress, Judge0 web or worker logs
  during the verification interval.

The initial runtime configuration was applied before a logging-probe result was
reviewed. No submissions occurred during that interval. Callbacks were temporarily
disabled, redaction was prepared and tested, then activation was repeated. Judge0
web was recreated for the logging configuration using the same image, ports and
resource settings; workers, Redis and PostgreSQL were not restarted.

The judge's stock request and SQL debug logs could expose signed callback URLs.
A persistent Compose override now mounts a small Rails configuration that filters
`callback_url` and sets INFO logging, suppressing SQL debug bind values while
retaining ordinary request/error logs. An invalid-language validation request
created no execution and verified `[FILTERED]` before real callback traffic.
See the infrastructure directory `environments/ec2-https/` for the configuration
and future deployment/rollback instructions.

## Resources and limits

The three synthetic accounts, their dedicated contest/problems/submissions and
all 17 recorded object keys were removed and verified absent. Twelve terminal
native Judge0 records remain under existing retention, with protected correlation
files in `perf/.private/`. No candidate resources were modified.

This is a small callback correctness/latency check. It does **not** establish
500–600-user capacity, a sustained judging rate, or deadline-burst performance.
The earlier [performance report](REPORT.md) describes the polling-only deployment
at its measurement time; it should not be read as the current callback setting.

[Machine-readable callback results](results/callbacks-enablement.json)
