# Request admission and execution bounds

Apply the database migrations before starting this build. Shared route admission and code replay fail
closed with HTTP 503 if PostgreSQL or the request limiter table is unavailable.
Known transient submission connection/pool/registration failures return JSON 503
with a one-second retry hint; unexpected failures remain JSON 500 and are logged.
HTTP 429 responses include `Retry-After` in seconds. Route budgets are shared in
PostgreSQL and survive API restarts; the pre-authentication ingress guard is local
to each API process and resets on restart.
Expired request-budget rows are removed after one day, at most once per minute per
process during successful admissions.

| Scope | Budget | Key |
| --- | --- | --- |
| Before authentication, including invalid tokens (per process) | 60,000 burst, 1,000/second refill | Client IP |
| Public contest/leaderboard/problem reads | 6,000/minute | Client IP |
| Authenticated contest/leaderboard/problem/session-status reads | 180/minute | Firebase UID |
| Submission status/details/history | 180/minute | Firebase UID |
| Profile reads | 180/minute | Firebase UID |
| Profile creation/updates | 60/minute | Firebase UID |
| Contest registration changes and tab-switch ingestion | 60/minute | Firebase UID |
| Tab-switch ingestion burst (per process) | 20 burst, 5/second refill | Firebase UID |
| Run and Submit burst (per process, before SQL) | 4 burst, 1/second refill | Firebase UID |
| Run and Submit requests | 60/minute | Firebase UID |
| Signup | 1,200/hour/IP and 100/10 seconds fleet-wide | IP and shared fleet key |
| Signup work (per process) | 8 active, 100 active + queued, 20-second queue wait | Capacity slots |
| Admin requests | 180/minute | Firebase UID |
| Consolidation and plagiarism report/pair reads | 6/minute | Firebase UID |

Authenticated limits apply after Firebase verification. Admin limits follow both
Firebase and admin-role checks. Public IP limits intentionally accommodate a campus
NAT shared by 600 participants; they do not replace participant limits. These
initial budgets need workload measurement before being raised. The ingress guard
reuses bounded in-memory token buckets (at most 10,000 client IPs). Below the
local ingress budget, missing/malformed-token requests return 401 without
acquiring a DB connection, including when the DB pool is exhausted. Valid
requests still pass their shared route budgets before business work; a route
budget DB error remains 503. The ingress guard needs no database access or pool-size adjustment.

The ingress guard is a per-process burst guard, not a network denial-of-service
protection or fleet-wide quota; use the proxy for a fleet-wide ceiling when
scaling. Firebase login and password-reset traffic bypasses this API and still
requires provider-side abuse controls.

Judge0 callbacks are excluded from the general database request limiter so invalid
callbacks must pass HMAC authentication before touching PostgreSQL. `/health` and
CORS preflights also bypass the request limiter.

## Proxy trust

The default IP source is the socket peer; `X-Forwarded-For` and `X-Real-IP` cannot
change it. Behind an ingress, set `TRUSTED_PROXY_CIDRS` to the exact comma-separated
CIDRs of proxies that can directly connect to the API. Example for a local ingress:
`TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128`. The proxy must append/overwrite forwarding
headers, and direct public access to the API port must be blocked. Never trust
`0.0.0.0/0` or `::/0`. Invalid CIDRs fail startup. Unconfigured proxy traffic shares
the proxy's generous IP budget, while authenticated requests retain UID budgets.

## Request bounds

JSON requests are limited to 1 MiB by default. Admin requests allow 8 MiB for
problem/testcase publication; signed Judge0 callbacks allow 12 MiB for encoded
execution output. Both known-length and streamed bodies are checked. HTTP header
reads time out after 5 seconds, complete reads after 30 seconds, response writes
after 90 seconds, and idle connections after 60 seconds; headers are capped at
32 KiB. Firebase token verification uses the request context with a 5-second cap.

Profile strings and admin problem/testcase arrays are bounded independently of the
body cap. A problem accepts at most 100 testcases, each input/output at most 1,048,576 Unicode characters,
subject to the total 8 MiB request bound. Existing larger problem definitions must
be split/reduced before publication through this API.

## Code submission admission and retries

Migration 38 is reserved for tab switching. Migration 39 adds durable code request
IDs; migration 40 adds shared HTTP budgets; migration 41 adds pending admission
slots. **Stop old API writers and finish in-flight preparation before applying
migration 41, then start the new API. Do not mix old and new writers.** Migration
41 backfills pending parents, adds a partial unique index on pending slots, and
removes the obsolete pending-user index. For rollback, stop and drain new API
writers, roll back migration 41, then start the old API. The down migration restores
the pending-user index; do not run new API writers on schema 40.

`SUBMISSION_MAX_PENDING` (default 600 across all contests) is enforced by independent
slot reservations; unrelated users no longer queue behind one global admission
lock. Per-UID/registration lock waits are bounded at 500 ms and return a one-second
429 retry hint on contention. All replicas must use the same capacity setting.
Completion, failure or deletion releases a slot through the partial index. An
unassigned legacy pending parent or a slot outside a newly lowered capacity blocks
fresh admission until those parents drain, even if the total count is below the
new cap. These checks reject ambiguous legacy capacity; they do not make mixed-version
writes safe.

The bound counts queued parents, not native judge concurrency. Each problem admits
at most 100 testcases. Admission precedes S3 reads; preparation failures release
the parent and recovery fails abandoned parents after 30 seconds. Source remains
limited to 64 KiB decoded / 128 KiB encoded, testcase and answer objects to 8 MiB
each. Scored jobs retain disabled networking, a 10-second wall limit and a 1 MiB
file limit alongside trusted CPU/memory settings.

The Submit cooldown defaults to five database-clock seconds per UID across all
contests. `SUBMISSION_RATE_LIMIT_SECONDS` must be positive to override the default;
zero/negative values no longer disable the service's protection. Cooldown
rejections return 429 with the remaining database-clock seconds in `Retry-After`.
A previous pending submission no longer blocks a fresh request after cooldown;
the interval is no longer tied to verdict latency. A full queue
still returns an advisory `Retry-After: 5`; completion time is unknown and the
hint does not guarantee available capacity.

Code clients should send a UUID `Idempotency-Key`. Its binding to UID, exact
payload and submission ID commits with admission. Reusing it returns the original
ID (including after the contest closes); changing its payload returns 409. The
website retains this key for every unsuccessful attempt and clears it only after
success. Code Submit automatically retries network failures and 429/500/502/503/504
responses at most twice (three attempts total), within one 30-second deadline
including token lookup. Retries honor `Retry-After`, add jitter and retain the
same payload, key and account. Account changes, cancellation, permanent errors,
or an exhausted budget stop retries. Run, MCQ, signup and registration POSTs are
not automatically retried. Reload/new-tab requests need a retained key to be
retries; a failed prepared submission replays as its original failed record.

## Signup bursts and work limits

Signup's shared fleet policy now admits 100 attempts per 10 seconds rather than
30 per second: a larger initial burst with a lower sustained rate. The shared
1,200/hour/IP budget remains for campus NATs. JSON is bound, validated and
charged against the per-IP budget before obtaining a process worker slot,
ensuring slow body reads cannot tie up active concurrency slots. Each API
process admits at most 100 active/queued requests with 8 active handlers.
After obtaining a worker, SQL budgets and Firebase/SQL work receive a
20-second context deadline; Firebase cleanup retains its separate 10-second
deadline. Body reads remain subject to the server's 30-second read deadline.
Queue waits are capped at 20 seconds; overflow/expiry returns 429 and
cancellation releases its reservation without calling Firebase.

These are initial safety budgets, not proof that 600 simultaneous signups or
600 native judging jobs meet latency targets. Larger bursts can still be rejected
intentionally. Recalibrate against measured Firebase/Judge0 latency; multiple
API replicas multiply the local concurrency limits while SQL budgets remain
shared. Admission errors now log their budget/stage and cause for diagnosis.

## Active EC2 deployment

Read-only SSH inspection on 11 November 2026 confirmed the recruitment API,
Caddy, Judge0 API/workers (`judge0/judge0:1.13.1`), PostgreSQL and Redis running as
Docker containers on EC2. The API container has a Docker limit of one CPU and
1 GiB memory. Judge0 API/worker containers have no Docker CPU/memory limit set;
this does not describe their per-execution Isolate limits. The Terraform
repository is inactive and no Terraform
change is part of these application PRs.

Judge0 currently publishes port 2358 on every IPv4/IPv6 host interface; the API
listens on port 8080. This does not establish external reachability: AWS security
groups and firewall rules were not verified. Restrict direct origin/Judge0 access
in the actual EC2 deployment and configure the trusted ingress addresses when
rolling out this API. No running container or production setting was changed.

The effective Judge0 API authentication, sandbox configuration, worker concurrency,
queue limits and network isolation still need deployment verification. Client
`enable_network:false` alone does not prove host isolation.

## Load-test report coverage

Compared with the 7 November 2026 Point Blank Recruitment load-test PDF, this
change adds shared signup/read/write admission, bounded scored backlog, JSON for
handled submission-creation failures, Retry-After, and code request replay. These
controls reject excess work intentionally with 429/503; they do not establish a
new signup success rate, remove every possible 500, or increase judge throughput.
Authenticated participants retain separate UID budgets behind a shared campus IP.

The separate [perf optimisation report](https://github.com/jayantkageriofficial/pb-recruitment-server/blob/perf/k6-judging-suite/perf/OPTIMISATION-20261107.md)
describes callback lock reduction and optional compile-once work. That implementation
is not included here. DB pool/worker tuning, native callback capacity, API CDN/WAF
protection, multiple-identity/nontrivial-code tests and the full contest soak remain
separate work. These application PRs do not put the recruitment API behind
Cloudflare. Native capacity for 500-600 participants remains unverified.

Before rollout, inspect schema_migrations and actual tables. A database that already
applied the former PR-only versions 38/39 needs explicit reconciliation; do not
force its version or run the renamed files blindly. Recovery supplies 10-second
wall and 1 MiB file limits for older snapshots that omit these settings; explicit
snapshot limits remain unchanged.

Migrations 39/40 were applied to the Neon production and development `neondb`
databases on 11 November 2026. Both were verified at version 40, dirty=false, with
the new tables/indexes and tab-switch tables present. Upgrade and rollback were
validated on a temporary production branch before completing the rollout. This
schema rollout did not deploy or restart the API or change ingress/sandbox settings.
