# Performance optimisation handoff

Start work from **`perf/k6-judging-suite`** in `pointblank-club/pb-recruitment-server`. This branch was created from `main` at `d32cbfb5d073dfb6c5166a85d98228ccd1c69e69` and adds the performance suite and reports, without application changes.

Implement justified optimisations, then measure them. Use the smallest complete change, official k6 documentation, and applicable `AGENTS.md` instructions.

## Read first

- [Callback retest and conclusions](CALLBACK-RETEST-20261107.md)
- [Machine-readable results](results/callback-retest-20261107.json)
- [Setup, credentials, workloads, thresholds and exact commands](README.md)
- [Callback deployment history](CALLBACKS.md)
- [Separate compile-once functional validation](reports/compile-once-validation-20261107.json), including its PR link

The measured production API revision was **`2efc783c2e87a407a4ac922e103b7d2b150491f1`**. Latest main includes the later problem-progress change (#86); the report's missing-progress observation describes the older deployed revision. Verify the effective deployment before comparisons. Do not describe historical measurements as tests of this newer main or the compile-once candidate.

## Environment

- Original workspace: `/home/hemang/pbrecruitment`
- Backend: `pb-recruitment-server`; frontend: sibling `pb-recruitment-website`
- API: `https://api.recruitment.pointblank.club`
- Read-only monitoring: `ssh -p 6622 ubuntu@13.205.163.28`
- Expected participation: 500–600 people; suite validated with **k6 v2.3.0**

Obtain approved authentication/SSH access securely. Credentials, private manifests, binaries and raw result directories are deliberately excluded from this branch. Configure the protected inputs described in README; create fresh fixtures rather than reusing cleaned historical manifests.

## Evidence and remaining problems

Callbacks are enabled. The corrected 30-minute participant journey passed all configured thresholds and recovery checks: 600 distinct authenticated users, 518 distinct submitters, 710 saved submissions and 3,914 finalized testcase jobs. The five-minute 600-user hold observed **0.83 scored submissions/s**, verdict p95 **12.61s**, receipt p95 **2.69s**, and read p95 **0.86s**. No dropped iterations, unresolved saved work or score mismatches were found. Low-load verdict p95 improved from 53.94s before callbacks to 12.44s afterward.

Remaining issues:

1. The passing journey still logged **355 HTTP-500/context-canceled callback attempts around the five-second timeout**, followed by successful delivery for every execution. Investigate `ExecutionStore.ProcessFinal`: it serializes parent callbacks and locks execution rows through multiple database operations. Measure lock/pool/query waits and shorten the durable transaction while preserving idempotency, aggregation, recovery and scoring.
2. The current path compiles C++/Java separately per testcase. Review the existing compile-once candidate. Its functional validation is separate evidence; it was not deployed or capacity-certified in these measurements. Preserve per-testcase time/memory limits and verdict/diagnostic correctness.
3. An earlier harness pacing defect clustered second submissions: 135 starts in an aligned 30-second window. That run is invalid as average-load evidence, but its queue failure was real: fleet queue 978, frontend timeouts, oldest test-owned pending age 141s and drain over 120s. All 764 saved submissions eventually finalized. The phase-preserving pacing fix and regression check must remain. The additional 10/s production deadline test was skipped after this failure.
4. A separate constant-arrival 0.833/s test completed all 250 submissions/1,375 jobs, but failed the retained proposed receipt target at 3.085s versus 3s. Do not relabel it as passing.

## Check configuration-only improvements too

The following settings were inspected read-only on EC2 on 7 November 2026. **No values were changed, and the proposed changes have not been benchmarked.** Recheck the effective runtime values before experimenting; blank Judge0 settings can resolve to defaults.

| File / setting | Observed value | Experiment or constraint |
| --- | --- | --- |
| `/home/ubuntu/judge0-v1.13.1/judge0.conf`: `CALLBACKS_TIMEOUT` | Blank; effective default 5 seconds | First isolated experiment: set `10`. It may reduce the observed callback cancellations/retries, but synchronous callbacks can occupy workers longer when the API is slow. Check queue age, drain and completed throughput as well as callback errors. |
| `/home/ubuntu/recruitment-server-config/.env`: `DB_MAX_IDLE_CONNS` | `5` | Separately try `25`, retaining `DB_MAX_OPEN_CONNS=25`. This may reduce connection churn; the benefit is unmeasured. Measure connection/pool behaviour where instrumentation permits. |
| API: `DB_MAX_OPEN_CONNS` | `25` | Keep the existing ceiling for the idle-pool comparison. Do not increase it without checking the database connection budget and measured waits. |
| Judge0: `COUNT` | Effective `16` on an eight-vCPU host | Compare `8`, `12`, and `16` in isolation, one configuration at a time. More workers are not automatically faster when compilation, CPU scheduling or callback contention dominates. |
| Judge0: `MAX_QUEUE_SIZE` | `1000` | Keep initially. A larger queue admits more waiting work without adding processing capacity. |
| API: `JUDGE0_TIMEOUT_MS` | `10000` | This is API-to-Judge0 request timeout, not the Judge0-to-API callback timeout. The measured Go client caps it at 10 seconds; setting a larger number does not extend it. |

Use the [version-pinned Judge0 configuration](https://github.com/judge0/judge0/blob/v1.13.1/judge0.conf), [callback implementation](https://github.com/judge0/judge0/blob/v1.13.1/app/jobs/isolate_job.rb#L272-L298), and the recruitment client's `internal/judge0/client.go` / `internal/db/db.go` as references. Keep callback authentication, sandbox limits and admission protections intact. Configuration tuning does not remove repeated compilation or per-parent callback locking.

Compare each change against an unchanged baseline using identical workloads, fixture costs and thresholds. Record the effective settings, final-verdict correctness, receipt/verdict latency, callback attempts, completed submissions/testcases, queue depth/age and recovery. Smoke first; increase load only after final outcomes and recovery pass. Report configuration-only results separately from code-optimisation results so their effects are attributable.

These files are loaded at process startup; editing them alone is not a hot reload. Applying changes to production requires Hemang's explicit authorization, an empty/drained queue and a controlled restart/recreation of only the affected services outside measurements. Do not restart Redis/PostgreSQL or apply these experiments through the read-only monitoring session. Keep the original values for rollback.

## Execution rules

- Inspect contracts and existing integration tests first. Distinguish mocked Judge0 tests from real sandbox execution.
- Implement locally and validate in isolation first. No production deployment, restart, scaling or configuration change without Hemang's explicit authorization. SSH monitoring stays read-only.
- Use dedicated synthetic accounts and a dedicated test contest. Never submit into recruitment contests or affect candidate rankings. Provision outside timing; track and clean only exact test-owned resources.
- Never automatically retry submission POSTs. Interrupted requests can have saved work; reconcile protected source markers and IDs. Preserve independent observation/drain after generation stops.
- Preserve grading, testcase coverage, sandboxing, callback authentication, admission rules and frontend polling semantics. Do not raise targets or reduce workload simply to obtain a pass. Diagnostic runs retained the 3s receipt pass target with a separate 5s hard receipt stop.
- Reverify instrumentation after optimisation. Participants, VUs, HTTP requests, submissions, testcase results and native Judge0 executions are separate quantities; compile-once may change their relationship.

Run smoke first, then baseline, controlled average arrivals and the corrected participant journey. Test deadline peaks, stress/breakpoint, faults and worker configurations in isolation, one change at a time. Run a representative 120-minute soak. If isolation is unavailable, preserve runnable suites and state exactly what remains unexecuted.

Confirm the actual contest configuration and problem costs: the public site advertised **10 MCQs, five coding problems and 120 minutes**, while none of the ten existing configurations matched that structure. The suite's average-rate hypothesis is 600 users × 10 scored submissions / 7,200s ≈ 0.833 submissions/s, averaging 5.5 testcases/submission. It does not cover the full real-contest MCQ/problem-cost distribution.

## Cleanup and deliverables

Historical cleanup is complete: **603 synthetic identities, both test contests, dependent application rows and all 1,975 object keys are absent**. Exactly **10,547 terminal native Judge0 records** from this retest remain under existing retention. Do not delete them through production SQL or change retention settings. The shared `run:capacity` limiter was untouched.

Deliver minimal fixes with meaningful regression tests, comparable before/after measurements, planned versus achieved work, final-verdict correctness, latency/throughput, callback failures, queue recovery, stopped stages, exact cleanup verification and a conditional go/no-go recommendation. Normal paced load passed; deadline capacity, 120-minute endurance, isolated stress/fault tests and large code/input/output volume remain uncertified. The bounded output-volume program was unit-checked only, not executed against the live judge.
