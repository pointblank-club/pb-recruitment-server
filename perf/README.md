# Recruitment judging performance suite

Run from `pb-recruitment-server`. This suite measures **real final verdicts**, not successful submission POSTs. No application code or deployed settings are changed. Read the [callback retest](CALLBACK-RETEST-20261107.md) for current results and [REPORT.md](REPORT.md) for the earlier polling-only measurements.

## Prerequisites and protected inputs

- Go 1.25.1+, Python 3.9+, Node, and **k6 v2.3.0**. Installed here at `/home/hemang/.local/bin/k6`.
- Existing protected server `.env` and managed Firebase service-account file, plus the sibling frontend `.env` for its public Firebase API key. Never put credentials in arguments or scripts.
- Fixture tools use the existing PostgreSQL stores and S3 client, and Firebase Admin creates only named synthetic identities. They never impersonate a real candidate or administrator. Registration and all timed traffic use the public API with those identities.
- `PERF_ENV_FILE=/absolute/protected/file` can select another backend configuration. `PERF_API`, `PERF_ENVIRONMENT=isolated`, and `PERF_FIREBASE_API_KEY` select the isolated target. Its API, database, object storage and Judge0 must all be isolated; changing the manifest's label alone does not create isolation.

Reproduce the installed Linux amd64 binary:

```bash
mkdir -p perf/.bin perf/.private
chmod 700 perf/.private
curl --fail --location https://github.com/grafana/k6/releases/download/v2.3.0/k6-v2.3.0-linux-amd64.tar.gz -o perf/.bin/k6.tar.gz
# Expected official archive SHA256:
# 39c3117b6af817592dcd0ce4242105c0a7af10948c2a425306f0be8f7a8a8ab1
printf '%s  %s\n' 39c3117b6af817592dcd0ce4242105c0a7af10948c2a425306f0be8f7a8a8ab1 perf/.bin/k6.tar.gz | sha256sum --check
tar -xzf perf/.bin/k6.tar.gz -C perf/.bin
export K6="$PWD/perf/.bin/k6-v2.3.0-linux-amd64/k6"
"$K6" version
go build -o perf/.private/fixtures ./perf/cmd/fixtures
node perf/check.mjs
python3 perf/validate.py
python3 -m unittest discover -s perf -p test_harness.py
go test ./perf/cmd/fixtures
```

The runner creates owner-only output directories and files. `.private/` holds credentials and correlation state; `results/<label>/` holds raw time series, correlation IDs, monitoring and reconciliation and is also ignored by Git. Do not enable HTTP debug logging or publish those directories. The committed report and results index contain aggregates only. Run IDs are identifiers; recorded `created` and metric timestamps supply actual times.

## Provision, measure, observe, clean

Provisioning, registration, token creation and history filling occur **before** timing. Use a fresh manifest path for every fixture pool. The tool refuses overwrite; after a partial failure use its recorded manifest for cleanup, then choose a fresh run ID. Future fixture publication pre-records deterministic problem IDs and exact object keys before writes.

```bash
mkdir -p perf/.private
chmod 700 perf/.private
export PERF_RUN_ID="pbperf-$(date -u +%Y%m%dT%H%M%SZ)-smoke"
PERF_ENVIRONMENT=production PERF_USERS=3 perf/.private/fixtures provision perf/.private/new-smoke.json
python3 perf/run.py smoke perf/.private/new-smoke.json
# Run only after reviewing smoke outcomes and queue recovery:
python3 perf/run.py baseline perf/.private/new-smoke.json
perf/.private/fixtures reconcile perf/.private/new-smoke.json
python3 perf/native_inventory.py perf/.private/new-smoke.json
perf/.private/fixtures cleanup perf/.private/new-smoke.json
perf/.private/fixtures verify-cleanup perf/.private/new-smoke.json
```

A test contest lasts 48 hours to allow separate provisioning, soak and post-run observation. That is a fixture lifetime, not the advertised contest duration. Every test problem scores only within this dedicated contest. Cleanup refuses foreign participants/rankings, pending work, foreign user activity, mismatched contest ownership and foreign object keys. It deletes exact recorded object keys, the exact test contest and its dependent rows, exact synthetic user/rate-limit rows, and exact Firebase UIDs. It leaves the shared `run:capacity` limiter untouched. Judge0's native retained records cannot be deleted through its disabled submission-delete API; preserve their protected token inventory. Do not delete candidate submissions or globally flush Redis.

For a fresh participant pool (the callback retest used 600 distinct managed identities):

```bash
export PERF_RUN_ID="pbperf-$(date -u +%Y%m%dT%H%M%SZ)-participants"
PERF_ENVIRONMENT=production PERF_USERS=600 perf/.private/fixtures provision perf/.private/participants.json
# Optional REAL pre-population: user 0 gets 41 scored submissions, one accepted then 40 WA.
# Refuses to seed an already-used small problem; verifies pages 20/20/1 through the API.
perf/.private/fixtures seed-history perf/.private/participants.json
# Initially the remaining 599 users have empty histories.
POPULATED=1 python3 perf/run.py expected perf/.private/participants.json
```

Provisioning 41 history items really executes 41 testcase jobs. It is excluded from throughput measurements. The suite reads pages 1 and 2 when `POPULATED=1`; only the seeded user is guaranteed to have those pages. This models a history-heavy subset, not 600 fully populated histories. `HISTORY_FANOUT=1` adds one history request per fixture problem on list visits; it is a conservative extra workload, not the verified current card behavior. Use `WORKLOAD=mixed HISTORY_FANOUT=1 POPULATED=1` to include it explicitly.

The runner refreshes expiring credentials outside measurement. Long tests refresh once per VU roughly every 50–55 minutes, with a deterministic 120-second spread, using protected refresh tokens. This small managed-auth traffic is tagged separately; any refresh failure aborts. It is not an authentication capacity test. Never reuse one token as 600 identities.

## Workloads and profile commands

`PROFILE` selects the test type. `WORKLOAD` selects the model independently; neither requires editing JS. All arrival iterations include submission **and verdict observation**, up to the frontend's timeout. A VU also remains occupied until six seconds after receipt if a verdict finishes sooner, respecting the currently deployed five-second per-user cooldown without delaying the independently scheduled business arrival rate. VUs therefore need to cover that waiting time. With a 60–75s iteration, 10 scored submissions/s needs roughly 750 VUs plus headroom for a sustained period; a 10s peak schedules only 100 iterations. `VUS` changes arrival allocation; provide at least that many distinct fixture users. `PARTICIPANTS` controls journey/burst users. They are separate from requests, submissions and testcase jobs. For an isolated 40/s hold with 60–75s waiting iterations, use roughly 3,000 distinct synthetic identities and `VUS=3000`; the fixture tool caps pools at 10,000. No such pool was created here. Credentials and metadata use k6 SharedArray storage to avoid copying the entire token pool into every VU.

| Profile | Default workload / generation | Planned scored work / logical testcase jobs | Exact invocation |
| --- | --- | --- | --- |
| smoke | 3 users, one iteration each, maximum 120s | 3 / 12, Python + C++ + Java | `python3 perf/run.py smoke FIXTURE` |
| baseline | 3 VUs, 2 arrivals/min for 300s | 10 / 55 | `python3 perf/run.py baseline FIXTURE` |
| expected | 50,100,300,500,600 participants; each 60s ramp + 300s hold | estimated 750 / 4,125 | `python3 perf/run.py expected FIXTURE` |
| expected arrival | 50 arrivals/min for 1,800s | 1,500 / 8,250 | `WORKLOAD=arrival python3 perf/run.py expected FIXTURE` |
| deadline | 600 arrivals/min for 10s | 100 / 550 | `python3 perf/run.py deadline FIXTURE` |
| stress | isolated, arrival rate increases 50→600→900→1,200→1,500/min over 1,200s | bounded by 6,000 submits | `python3 perf/run.py stress ISOLATED_FIXTURE` |
| spike | isolated, each of 600 users submits once over 10s | 600 / 3,300 | `python3 perf/run.py spike ISOLATED_FIXTURE` |
| soak | isolated, paced 600-user journey for 7,200s | estimated 6,000 / 33,000 | `python3 perf/run.py soak ISOLATED_FIXTURE` |
| breakpoint | isolated staircase of independent ≥300s constant-rate holds, drain between holds, stop at first failed hold | ≤18,000 total POST attempts | `python3 perf/capacity.py ISOLATED_FIXTURE` |
| breakpoint ramp | isolated exploratory ramp, separate from sustained certification | ≤18,000 submits | `python3 perf/run.py breakpoint ISOLATED_FIXTURE` |
| volume | isolated, full matrix and up to 60k source bytes; custom-input size tested separately | 12 submits / 47 jobs with the shown overrides | `MATRIX=1 RATE_PER_MINUTE=2 DURATION_SECONDS=360 MAX_SUBMISSIONS=12 CODE_PADDING_BYTES=60000 python3 perf/run.py volume ISOLATED_FIXTURE` |
| scalability | isolated, 50 arrivals/min for 300s | 250 / 1,375 | `python3 perf/run.py scalability ISOLATED_FIXTURE --label workers-config-a` |
| recovery | isolated 600-user/10s burst + bounded drain | 600 / 3,300 | `python3 perf/run.py recovery ISOLATED_FIXTURE` |
| resilience | isolated, 6 arrivals/min for 300s through fault proxy | 30 / 165 | `python3 perf/run.py resilience ISOLATED_FIXTURE` |
| fairness | isolated mixed arrival/custom-run/history traffic for 300s | 250 submits / 1,375 jobs | `HISTORY_FANOUT=1 POPULATED=1 python3 perf/run.py fairness ISOLATED_FIXTURE` |

Replace `FIXTURE` with an absolute or repository-relative protected manifest path. Production runner permits only smoke, baseline, expected and deadline; all other profiles reject the public production target. A failed run does not authorize increasing load. Inspect its final outcomes before manually proceeding. The capacity driver has that stop gate built in and reports the highest **passed, observed** rate, or null.

Additional independent selectors:

```bash
# 1–3-user correctness checks; 0=Python AC, 1=C++ AC, 2=Java AC, 3=WA,
# 4=CE, 5=RTE, 6=TLE, 7=memory/RTE, 8=moderate AC, 9=20-case C++ AC,
# 10=MCQ AC, 11=MCQ WA, 12=4096-byte output WA. Use fresh MCQ answers.
PARTICIPANTS=1 MATRIX=1 PROGRAM_OFFSET=4 python3 perf/run.py smoke FIXTURE
# Separate custom-run smoke; explicit one-extra-call admission probe per user:
PROBE_RUN_REPEAT=1 python3 perf/run.py smoke FIXTURE --workload runs
# While a saved scored submission is pending, try one optional run:
PARTICIPANTS=1 PROBE_RUN_BUSY=1 python3 perf/run.py smoke FIXTURE
# Separate custom-run/input-volume traffic, respecting observed Retry-After:
RATE_PER_MINUTE=30 INPUT_PADDING_BYTES=60000 python3 perf/run.py baseline FIXTURE --workload runs
# Isolated output-volume check: one bounded 4096-byte stdout, expected WA.
MATRIX=1 PROGRAM_OFFSET=12 RATE_PER_MINUTE=2 DURATION_SECONDS=30 MAX_SUBMISSIONS=1 python3 perf/run.py volume ISOLATED_FIXTURE
# Journey / arrivals / burst / runs / mixed, with all optional read amplification:
HISTORY_FANOUT=1 POPULATED=1 python3 perf/run.py expected FIXTURE --workload mixed
```

`PROBE_RUN_REPEAT` is an explicit bounded negative admission check. It intentionally makes a second call after a successful run, rather than retrying a timeout. Rejected runs are separate counters, never completed executions. `PROBE_RUN_BUSY` covers the pending-score gate. A 429 response does not identify whether cooldown, fleet capacity, backlog, or Judge0 queue fullness caused it, so the harness reports `capacity_or_cooldown` rather than inventing a reason. It never retries submission POSTs; uncertain POSTs are reconciled from the exact test contest and fail the run for review. A custom-run timeout remains uncertain because the API returns no run token.

The journey browses contest/problems, opens a code statement/history, thinks 30–60s, optionally runs custom input (20% of users), submits once, uses the frontend poller, fetches details, revisits problems/leaderboard, and paces toward one coding submission/720s. New users browse immediately on joining, then stagger thinking over that interval. Each user's initial phase is preserved on later cycles; the callback retest retains a failed earlier run whose phase reset accidentally clustered second submissions. It models coding activity, not all advertised MCQ answers; MCQ grading is covered by the correctness matrix. A full contest MCQ traffic distribution remains an explicit modeling gap.

## Rate arithmetic and budgets

`plan.json` records configuration, bounds and estimates before each run. Actual rates come from metric/event timestamps and reconciliation, not executor labels. `summary.json` contains all metrics and thresholds; `report.json` contains planned/started/saved/completed work, dropped iterations, admission counts, verdict mismatches, unresolved work, recovery and observed rates.

Assumptions: 600 people × 10 coding submissions / 7,200s = **0.8333 scored submissions/s**. The regular deterministic C++/Python/Java/moderate cycle has 1,10,1,10 testcases, averaging **5.5 jobs/submission**, hence **4.5833 logical testcase jobs/s** at that average. A 10/s deadline hypothesis implies 55 jobs/s, independently of the participant count. A pure 10-case problem implies 8.333 average or 100 peak jobs/s. Every case is compiled/executed separately by Judge0; this is not one compilation plus ten executions.

With P status polls, D=1 detail read and B browsing/history reads per submit, HTTP rate = scored rate × (1 + P + D + B), plus optional custom/MCQ traffic. The measured baseline made 115 polls for ten submissions (11.5 each), so future planning defaults to P=12. With B=0 this gives 11.667 HTTP requests/s at the normal steady average, or 140/s if a 10/s submission rate were sustained long enough to reach steady polling. A 10s/100-submission deadline burst instead plans 1,400 total requests spread across generation and later verdict observation; 140/s is not its measured or predicted instantaneous peak. `ASSUMED_POLLS` changes planning only; actual `polls_per_submission` is measured. Journey planning uses B=8 (or 20 with conservative six-problem history fan-out); these are estimates, and actual endpoint counts in JSON are authoritative. Custom runs add at most one logical judge job each admitted execution, not one per HTTP attempt. Closed journey iterations and short staged holds can achieve less than the paced estimate because thinking, first-visit staggering, compilation and verdict waiting consume time.

Overrides: `DURATION_SECONDS`, `RATE_PER_MINUTE`, `PARTICIPANTS`, `VUS`, `MAX_SUBMISSIONS`, `OBSERVATION_SECONDS`, `MATRIX`, `PROGRAM_OFFSET`, `CODE_PADDING_BYTES`, `INPUT_PADDING_BYTES`. Source/stdin padding is capped at 60,000 bytes; run validation remains enabled. Never raise a work budget to ignore a failed stage. Reaching the work cap aborts generation and leaves observation to the independent runner; skipped higher stages cannot pass. Maximum started POSTs is bounded independently of response slowdown; no hidden POST retries exist. Stress/breakpoint ramp figures are exploratory; only the capacity driver's independent held-and-reconciled steps support a sustained-rate result.

`planned_primary_judge_jobs_estimate` includes the estimated custom-run work. `planned_primary_judge_jobs_upper_bound` instead uses the iteration budget, maximum selected testcase count and maximum optional custom calls per iteration. For a regular journey capped at 1,000 iterations, the conservative bound is 10,000 scored testcase jobs plus 1,000 custom jobs; admitted custom work is normally much smaller. These are logical primary jobs; extra physical retry dispatches are reported separately. Older saved plans used the upper-bound field name for an estimate; those historical artifacts remain unchanged.

## Proposed thresholds and independent recovery

No repository judging SLO was found. These defaults are **assumptions**, not an existing service commitment:

| Measure | Proposed pass condition | Override |
| --- | --- | --- |
| read latency | p95 < 2,000ms | `READ_P95_MS` |
| submission receipt | p95 < 3,000ms | `RECEIPT_P95_MS` |
| start→observed final verdict | p95 < 60,000ms | `VERDICT_P95_MS` |
| expected verdict + detail identity/results | 100% correct | fixed |
| unexpected API failures | <1% | `ERROR_RATE` |
| authentication / detail failures | zero | fixed |
| frontend-unresolved saved work | zero | fixed |
| scored admission rejections | zero | `SCORED_REJECTION_BUDGET` |
| dropped iterations | zero | fixed |
| final saved submissions unresolved | zero | fixed |
| test backlog drain upper bound | ≤120s from last receipt | `QUEUE_RECOVERY_SECONDS` |
| oldest test-owned pending age during generation | ≤90s, otherwise stop generator | `MAX_PENDING_AGE_SECONDS` |

k6 thresholds use `abortOnFail`, evaluated after 30s by default (`ABORT_DELAY`). This can stop observers inside k6, so Python continues independent database observation for up to 180s (`OBSERVATION_SECONDS`) after generation exits or aborts. All saved submissions in the exact test contest are reconciled, including work for which a POST response was lost. Unknown/ambiguous work is explicitly reported and fails; it is never blindly resubmitted. Every pool gets separate run-ID-prefixed protected reconciliation, execution-token and database-summary files. The database audit verifies final execution counts and score totals based on whether each problem has **any accepted** submission, so later WA/CE/RTE cannot erase its score.

`RECEIPT_ABORT_P95_MS=5000` is an explicit diagnostic mode: it retains the proposed 3s receipt pass condition while moving only the receipt hard stop to 5s. Such a run still fails if p95 exceeds 3s. Correctness, authentication, frontend timeout, read latency, work budget and independent queue guards stay active. `health_guards_passed` never substitutes for `passed`. The callback retest documents why this mode was used after strict receipt-only aborts.

After an interrupted POST, wait for independent observation and reconcile its exact source marker without submitting again:

```bash
python3 perf/reconcile_posts.py FIXTURE perf/results/LABEL
python3 perf/callback_audit.py FIXTURE perf/results/LABEL
```

These tools keep individual IDs and recovered source correlations in protected artifacts. The callback audit reports failed attempts as well as eventual successful delivery.

`test_backlog_drain_upper_bound_seconds` uses timestamped test-owned pending counts; it is a sampled upper bound, not queue wait. `completed_testcases` counts persisted testcase results. Physical duplicate dispatches are reported separately by `extra_dispatch_attempts`; logical jobs are not claimed to be an exact physical CPU count when a dispatch response is lost.

## Read-only monitoring and unavailable instrumentation

The runner starts `monitor.py` over the provided SSH connection and samples local generator `/proc` counters, sockets and file descriptors. It does not restart, scale, deploy or change configuration. For manual read-only sampling:

```bash
ssh -p 6622 ubuntu@13.205.163.28 'sudo -n python3 - 30' < perf/monitor.py > perf/.private/host-samples.jsonl
perf/.private/fixtures snapshot FIXTURE
# Independent observation after interruption:
perf/.private/fixtures reconcile FIXTURE
```

Host samples include CPU tick counters/load/run queue, memory/swap, disk and network counters, container utilization/limits/restarts/OOM/throttling, Judge0 `/workers`, Judge0 DB queued/running counts and oldest pending age, Redis PING and PostgreSQL connection health. Local samples include k6 CPU ticks, RSS, thread/file descriptor counts, host load and socket state. Deltas in cumulative counters support CPU/IO/network trends; k6 records TCP/TLS/blocked time and dropped iterations separately. Generator samples originate on this workstation, separate from the judge host.

API DB samples include aggregate database connections/activity, transaction/block counters, and test-owned pending/tokenless/retry backlog. **Go `sql.DB` pool in-use/waits and per-query latency are not exported by the deployed API.** PostgreSQL block timings can be zero when timing instrumentation is disabled; do not infer zero IO latency. The callback audit reports HTTP request durations and statuses from API logs; lock waits and transaction execution are not separated. Actual Judge0 queue wait and compilation durations are unavailable. `reported_execution_ms` is the API's stored Judge0 runtime (sum across scored cases); it excludes queue/compilation. Do not subtract unrelated host clocks to invent missing durations. `/workers` reports zero registered workers even while execution succeeds on this deployment, so its active-worker field is not a validated capacity measurement. The monitor supports this host's hybrid cgroup CPU counters and records oldest queued-job age separately from oldest pending-job age.

## Isolated resilience, worker comparisons and direct Judge0

No usable isolated Docker daemon was available in this session. The following are delivered and unit-tested locally, **not executed against real isolated services**.

Use an already isolated API/Judge0 pair reachable on loopback. Copy `fault-config.example.json` into protected storage and edit its ports/run ID. Before measuring that isolated deployment, set its `JUDGE0_URL=http://127.0.0.1:23590/judge` and callback base `http://127.0.0.1:23590/callback`, using its existing protected callback secret. Never do this on production. The proxy forwards signatures without printing them.

```bash
python3 perf/fault_proxy.py perf/.private/fault-config.json
# In another terminal, with isolated fixture:
python3 perf/run.py resilience ISOLATED_FIXTURE
```

Run separately with `callbacks` = `normal`, `delayed`, `missing`, or `duplicate`; `judge_failures` = 2 for transient dispatch 503s. `fault_seconds` limits the fault interval (default 30s); delayed callbacks are capped at 30s. The normal recovery poller remains enabled. For worker interruption, the **local** Docker container must be named `<run_id>-...` and labelled `pbperf.run_id=<run_id>` and `pbperf.environment=isolated`:

```bash
python3 perf/interrupt_worker.py RUN_ID RUN_ID-workers --seconds 15
```

This pauses then resumes in `finally`, without SSH or production changes. Observe queue drain and correctness through the regular runner. Compare two separately approved isolated worker configurations by running identical `scalability` commands with different labels; change one worker parameter between runs, outside measurement, and retain both host limit snapshots. Neither script changes worker count.

For a separate direct-Judge0 bottleneck profile, use an existing private loopback route or pre-approved SSH tunnel to an **isolated** judge, never expose a port:

```bash
FIXTURES=/absolute/isolated.json PRIVATE_JUDGE=http://127.0.0.1:12358 "$K6" run --out json=perf/.private/direct.jsonl perf/direct-judge.js
```

Direct Judge0 data is excluded from application throughput. No direct test was run here. A timed-out direct POST can still create work; reconcile it on that isolated judge before reuse.

## Official k6 references

- [Scenarios and executor selection](https://grafana.com/docs/k6/latest/using-k6/scenarios/)
- [Arrival-rate VU allocation](https://grafana.com/docs/k6/latest/using-k6/scenarios/concepts/arrival-rate-vu-allocation/)
- [Graceful stop and interrupted iterations](https://grafana.com/docs/k6/latest/using-k6/scenarios/concepts/graceful-stop/)
- [Thresholds and abortOnFail](https://grafana.com/docs/k6/latest/using-k6/thresholds/)
- [Built-in metrics, including dropped iterations](https://grafana.com/docs/k6/latest/using-k6/metrics/reference/)
- [Test lifecycle](https://grafana.com/docs/k6/latest/using-k6/test-lifecycle/)
