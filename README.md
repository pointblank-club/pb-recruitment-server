# How to run?
- Use Go 1.25.1 or newer and configure `.env` using `.env.example`.
- Apply the database migrations before starting the server (see below).
- Install [air](https://github.com/air-verse/air?tab=readme-ov-file#installation)
- Run `air` command in the terminal
- Alternatively, run `go run ./cmd/app` without installing Air.
- Server is running on `http://localhost:8080`
- API documentation (Swagger UI): `http://localhost:8080/swagger/index.html`
- Raw JSON Spec: `http://localhost:8080/swagger/doc.json`
- Enable Swagger with `STAGE=dev` or `ENABLE_SWAGGER=true`; otherwise these URLs return 404.

## API Documentation (Swagger)

To regenerate Swagger documentation after modifying route annotations or DTOs:

```bash
make swagger
```

## DB Migrations

- Install [migrate](https://github.com/golang-migrate/migrate)
- Add `DB_ADDR` to `.env`
- Run these commands from the backend directory. Include `$HOME/go/bin` in `PATH`
  if `migrate` was installed with `go install`.
- The current schema requires migrations through `000030`: submission cooldowns
  (`000027`), saved final ranks (`000028`), internal compiler diagnostics (`000029`),
  and Judge0 recovery state (`000030`).
  `make migrate-up` applies only pending migrations; do not force the version or
  roll back a production database.

### Migration Commands

```bash
# Create a new migration
make migration create_users_table

# Run migrations up
make migrate-up

# NOT AVAILABLE ON PROD - Rollback migrations (specify number of steps)
make migrate-down 1

# NOT AVAILABLE ON PROD - Force migration version (use with caution)
make migrate-force 1
```

## Problem authoring

### Object storage

Problem descriptions, coding testcases, expected outputs, and submitted code use
the private bucket configured by `S3_SUBMISSIONS_BUCKET`. For Neon object storage,
set `AWS_ENDPOINT_URL_S3` to the branch's S3 endpoint, `AWS_REGION` to its storage
region, `AWS_ACCESS_KEY_ID` to a credential's `token_id`, and
`AWS_SECRET_ACCESS_KEY` to its `s3_secret_access_key`. The existing client enables
path-style requests when an endpoint is supplied. Keep credentials in `.env`;
use a credential scoped to the intended branch with storage read/write access.

The local setup checked on 14 October 2026 uses the `PointBlankRecruitment`
project's **development** database and storage branch (`br-young-base-a1ey83m1`),
private bucket `rec`, and storage region `ap-southeast-1`. The supplied local
`DB_ADDR` does **not** point to the branch named `production`. Verify the database
hostname and storage branch together before running migrations or deploying.

### Editing problems

Finish problem edits and deletion before the contest starts. New update and delete
requests return 409 once the contest starts, including after it ends, to preserve
verdicts and leaderboard scores. Finish any in-flight edits before opening the round.
The start time cannot be changed once the contest starts; end-time extensions remain allowed.
Correcting a started contest requires controlled regrading and score reconciliation.

### Testcase caching

Testcase/answer pairs share concurrent loads and cache successful reads per replica:
64 entries, 32 MiB of accounted key/slice/string data, five-minute TTL from loading.
Expiry is checked on access; eviction removes the oldest load. These limits do not
bound in-flight downloads or total process memory. Failures and oversized results
are not cached. Shared loads have a five-second timeout independent of each caller;
callers can cancel their own wait. Returned slice containers are copied.
Problem edits publish both objects under a new version directory before updating the
problem key, so replicas cannot mix old inputs with new answers. Hidden cases remain
server-side. Old versions remain until problem deletion; legacy objects overwritten
outside the edit API may stay cached for five minutes.

Keep problem editing paused until all backend replicas and the website editor are
updated. Reload open admin tabs before resuming edits. Older backends read answers
from the canonical path and cannot safely serve
versioned edits. After any versioned edit, rollback must retain version-aware readers
or migrate references and both objects while traffic is stopped. Deploy the backend
before the website editor, which now consumes inputs and answers from one response.

## PostgreSQL tests

Use a disposable PostgreSQL 12+ database. The tests create isolated schemas, apply
all migrations through `000030`, and clean up afterward. They skip when
`TEST_DATABASE_URL` is unset.

```bash
TEST_DATABASE_URL='postgres://user:password@localhost/test_db' go test -race ./internal/stores -count=1
```

## Judge0 deployment & Environment configuration

- Apply migrations through `000030` to the target database before deploying this build; use PostgreSQL 12+.
- Set `SUBMISSION_RATE_LIMIT_SECONDS` (default: 5s) and `LEADERBOARD_CACHE_TTL_SECONDS` (default: 3s, max: 60s) identically across backend replicas.
- Set `JUDGE0_URL` and `JUDGE0_AUTH_TOKEN`. Keep the Judge0 API inaccessible to contestants.
- Enable callbacks with `JUDGE0_CALLBACK_BASE_URL` and the same `JUDGE0_CALLBACK_SECRET`
  on every backend replica. Verify a signed callback reaches the backend from Judge0.
- For a local backend without an inbound callback address, leave both callback
  settings empty. The existing recovery worker fetches results every 15 seconds;
  callbacks are not required for this local setup.
- With Vivek, load-test queue capacity for 500–600 contestants. The instance checked
  on 11 October has `max_queue_size=100` and `max_memory_limit=512000` KB; our default
  256 MiB is 262144 KB. Raising the queue limit also requires enough workers.
- Execution inputs are saved before dispatch. Recovery uses short PostgreSQL claims
  (`FOR UPDATE SKIP LOCKED`), then makes network requests outside the transaction.
  Leases expire after 30 seconds; sweeps run every 15 seconds. Retry state is shared
  across replicas: at most three tokenless dispatch attempts or three consecutive
  failed result observations, with backoff, before `judge_error`.
- Queued/running responses reset the failure counter. Wrong answers, compilation,
  runtime and time-limit errors finalize immediately. Recovery ignores `/workers`
  and job age; persistently queued jobs require operator investigation.
- Callback signatures bind the dispatch attempt. Stale callbacks and expired claims
  cannot replace newer results or award duplicate scores. Replace old backend replicas
  together after migration `000030`; keep callback secrets identical.
- Use the stock Judge0 API and workers. The initial 30-second lease gives callbacks
  time to arrive before retrying a tokenless dispatch; subsequent tokenless retries
  wait at least 30 seconds, with backoff. A callback received during this grace
  period can complete the execution without another POST.
- shortcut: an accepted request with a lost response and no timely callback may
  execute twice on retry; add provider-supported idempotency only if duplicate CPU
  work becomes unacceptable. Attempts are bounded and scoring remains idempotent.
- Keep callback signatures out of proxy and load-balancer logs as well as application
  logs. The signature is in the callback URL query string.

Verdicts map status 6 to compilation error and 13/14 to `judge_error`. Judge0 has no
dedicated memory-limit verdict. Submission runtime is the sum of testcase runtimes;
memory is the maximum. Scores come from solved problems; freeze problem scores
before starting and use controlled reconciliation for manual database adjustments.

Judge0 1.13.1 miscompares null stdout with blank expected text. Shared finalization
corrects this exact case from the saved expected output. Other wrong answers, stale
attempts, and terminal results remain unchanged. Legacy callbacks without snapshots
save their token and defer grading to recovery. Historical verdicts need explicit regrading.

For live verification, supply `LIVE_JUDGE0_URL` and a disposable `TEST_DATABASE_URL`
to `go test -race ./internal/stores -run TestLiveJudge0 -count=1`. Callback checks also
need `LIVE_JUDGE0_CALLBACK_BASE_URL` routed from the worker to `127.0.0.1:23582`.

### Local connection to the EC2 judge

The judge at `ubuntu@13.205.163.28` is reachable over SSH on port `6622`.
Its HTTP port `2358` was not directly reachable from the development machine on
14 October 2026. Forward it to localhost and keep the tunnel running:

```bash
ssh -NT -p 6622 \
  -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
  -L 127.0.0.1:2358:127.0.0.1:2358 ubuntu@13.205.163.28
```

Use these backend `.env` settings:

```dotenv
JUDGE0_URL=http://127.0.0.1:2358
JUDGE0_AUTH_TOKEN=
JUDGE0_CALLBACK_BASE_URL=
JUDGE0_CALLBACK_SECRET=
JUDGE0_TIMEOUT_MS=10000
JUDGE0_BATCH_SIZE=20
SUBMISSION_RATE_LIMIT_SECONDS=5
LEADERBOARD_CACHE_TTL_SECONDS=3
```

The empty API token matches the inspected Judge0 configuration; access in this
setup is through SSH. If Judge0 authentication is enabled, supply its matching
token. Do not expose an unauthenticated Judge0 API publicly. A deployed backend
must use its own reachable Judge0 address or maintained tunnel; localhost here
refers only to the development machine running the SSH forward.

Check the connection before starting the backend:

```bash
curl --fail http://127.0.0.1:2358/about
curl --fail http://127.0.0.1:2358/workers
```

On 14 October 2026, the inspected judge ran version `1.13.1` with a batch limit
of 20, queue limit of 100, and maximum memory limit of 512000 KB. An empty
`/workers` list does not prove workers have stopped: this Judge0 endpoint lists
registered queues. Verify an actual execution before treating the judge as ready.
Python and C++17 smoke executions both returned `Accepted` through the SSH tunnel
on that date. `/workers` then reported zero available workers despite those
successful executions. Recovery therefore ignores these statistics.

The database referenced by the local `DB_ADDR` was checked on 14 October 2026:
its migration version was `29`, `dirty=false`, and migration `up` reported
`no change`. Verify the migration version again for each deployment.
