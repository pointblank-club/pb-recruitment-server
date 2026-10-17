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
- The current schema requires migrations through `000031`: submission cooldowns
  (`000027`), saved final ranks (`000028`), internal compiler diagnostics (`000029`),
  Judge0 recovery state (`000030`), and ordered recovery claims (`000031`).
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

## PostgreSQL tests

Use a disposable PostgreSQL 12+ database. The tests create isolated schemas, apply
all migrations through `000031`, and clean up afterward. They skip when
`TEST_DATABASE_URL` is unset.

```bash
TEST_DATABASE_URL='postgres://user:password@localhost/test_db' go test -race ./internal/stores -count=1
```

The opt-in contest load test starts 600 simultaneous submission requests with ten
testcases each, then checks polling and duplicate callbacks racing three recovery
workers. It uses real PostgreSQL and simulated S3/Judge0; it does not certify the
production Judge0 fleet. It uses the configured database pool (defaults: 25 open/idle).

```bash
RUN_JUDGE0_LOAD_TEST=1 TEST_DATABASE_URL='postgres://user:password@localhost/test_db' \
  go test -race ./internal/stores -run '^TestJudge0ContestLoadPostgres$' -count=1 -v
```

## Judge0 deployment & Environment configuration

- Apply migrations through `000031` to the target database before deploying this build; use PostgreSQL 12+.
- Set `SUBMISSION_RATE_LIMIT_SECONDS` (default: 5s) and `LEADERBOARD_CACHE_TTL_SECONDS` (default: 3s, max: 60s) identically across backend replicas.
- Set `JUDGE0_URL` and `JUDGE0_AUTH_TOKEN`. Keep the Judge0 API inaccessible to contestants.
- Use localhost or a private network for production Judge0 traffic. SSH forwarding is for local verification.
- Enable callbacks with `JUDGE0_CALLBACK_BASE_URL` and the same `JUDGE0_CALLBACK_SECRET`
  on every backend replica. Verify a signed callback reaches the backend from Judge0.
- For a local backend without an inbound callback address, leave both callback
  settings empty. The existing recovery worker fetches results every 15 seconds;
  callbacks are not required for this local setup.
- Size the Judge0 queue and worker fleet for participants × testcases × submission waves,
  plus existing backlog. For 600 contestants with ten testcases, one wave is 6,000 jobs;
  the inspected queue limit of 100 cannot absorb that burst. Measure completion time on staging.
- Source must decode to valid UTF-8; malformed source returns HTTP 400 before submission creation.
- Execution inputs are saved before dispatch. Recovery claims rows with PostgreSQL
  `FOR UPDATE SKIP LOCKED` and makes network requests after releasing the locks.
  Claims last 30 seconds; sweeps run every 15 seconds. Polling and tokenless dispatch
  run independently, so a slow POST cannot block healthy results.
- Retry budgets are shared across replicas: three dispatch reservations or three consecutive
  failed result observations before `judge_error`. Crashes and initially unsent chunks can
  consume dispatch reservations. The initial reservation gives callbacks 30 seconds to arrive;
  subsequent ambiguous attempts wait at least 30 seconds after dispatch, with backoff.
- Saving the first token resets the observation budget; duplicate deliveries do not.
  Queued/running observations reset failures. Contestant errors finalize immediately.
  Recovery ignores `/workers` and job age; persistently queued jobs need operator investigation.
- Polling uses `JUDGE0_BATCH_SIZE` (default 20) and applies each batch's results concurrently.
  Keep `DB_MAX_OPEN_CONNS=25` and `DB_MAX_IDLE_CONNS=25`; update older five-idle configurations
  and account for every replica's pool when sizing PostgreSQL's connection limit.
- Routine polling omits stdout and preserves compiler/stderr diagnostics. Only Wrong Answer
  jobs that might match the blank-output defect fetch stdout. Responses are bounded at 12 MiB
  per job for two stock 4 MiB diagnostic files plus Base64/JSON; increase the bound if provider
  file limits grow.
- Callback signatures bind the dispatch attempt. Stale callbacks and replaced claims cannot
  overwrite newer results or award duplicate scores. Replace old replicas together after
  migrations through `000031`; keep callback secrets identical.
- Use stock Judge0 API and workers. A lost response without a timely callback can cause
  duplicate physical execution on retry. The budget bounds this; scoring remains idempotent.
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

Forward the judge's private HTTP port over SSH for local verification:

```bash
ssh -CNT -p 6622 \
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

Supply `JUDGE0_AUTH_TOKEN` if API authentication is enabled. Keep the API private;
`localhost` above refers to the machine running the tunnel. Deployed backends need
an address reachable from their own network.

Check the API version, then run the live tests above to verify worker execution:

```bash
curl --fail http://127.0.0.1:2358/about
```

An empty `/workers` response does not prove execution is unavailable. Verify the
target database's migration version before each deployment.
