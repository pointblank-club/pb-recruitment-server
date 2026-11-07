# Callback retest — 7 November 2026

**Callbacks substantially improved normal judging latency. The corrected 600-user paced journey passed all configured thresholds and recovery checks. Peak/deadline readiness remains unproven: an earlier clustered workload caused frontend timeouts and a long queue.** No application code, deployment, worker count, resource limit, admission rule or security setting was changed by this retest.

## Demonstrated result

The 30-minute journey ramped through 50, 100, 300, 500 and 600 distinct authenticated synthetic users, with a 60-second ramp and five-minute hold at each level. All 600 identities made requests; **518 distinct users submitted**. It saved **710 submissions and finalized 3,914 testcase jobs**, with every expected verdict/detail check correct, zero dropped iterations, zero unresolved saved work, and zero score mismatches.

| Participant stage | Receipt p95 | Observed verdict p95 | Read p95 | Actual submission starts/s during the full five-minute hold |
| --- | ---: | ---: | ---: | ---: |
| 50 | 1.96s | 12.02s | 0.70s | 0.060 |
| 100 | 2.20s | 12.16s | 0.79s | 0.133 |
| 300 | 2.46s | 12.45s | 0.78s | 0.400 |
| 500 | 2.73s | 12.40s | 0.87s | 0.663 |
| 600 | **2.69s** | **12.61s** | **0.86s** | **0.830** |

The 600-user hold observed **249 completed scored submissions and 1,383 testcase results in 300 seconds: 0.83 submissions/s and 4.61 testcase results/s**. This is the highest sustained rate observed in a fully passing held stage here, under this specific paced workload; it is not a measured capacity ceiling or a 120-minute certification.

Across the whole corrected profile, receipt/verdict/read p95s were **2.59s / 12.50s / 0.84s**. Planned work was an estimate of 750 submissions/4,125 scored jobs/11,400 HTTP requests; achieved work was 710/3,914/**11,105 HTTP requests**, with 2.81 status polls/submission. All five stages completed. Including graceful stop and reconciliation, the window was 1,992.79s, giving conservative whole-window completion rates of 0.356 submissions/s and 1.964 testcase results/s; those averages include the ramps and idle grace period. The highest aligned 30-second submission-start bucket was **28**, not perfect simultaneity.

The sampled test backlog cleared within **13.61s after the last receipt**; oldest test-owned pending age was **13.40s**. Fleet Judge0 queue depth peaked at **59**, with oldest queued-job age **4.70s**. There were 137 custom-run attempts: **16 successful executions and 121 expected 429 rejections**, counted separately from scored judging.

## Other runs and failures retained

| Measurement | Planned / saved submissions | Final testcase jobs | Result |
| --- | ---: | ---: | --- |
| Python/C++/Java smoke | 3 / 3 | 12 | Passed; verdict p95 12.02s. |
| Repeated five-minute low baseline, 2/min | 10 / 10 | 55 | Passed; verdict p95 **12.44s**, versus **53.94s** in the earlier polling-only baseline. |
| WA / compilation / runtime / time / memory fixtures | 5 / 5 | 5 | All expected failure verdicts verified. Memory exhaustion maps to `rte` on this deployment. |
| Two-minute arrival check, 12/min | 24 / 24 | 132 | Passed; receipt/verdict p95 1.88s / 12.29s. |
| Strict arrival, 50/min | 250 / 31 | 166 | Stopped early on proposed 3s receipt target; p95 3.17s. All saved work finalized correctly. |
| Strict arrival, 30/min | 150 / 38 | 209 | Stopped early on receipt p95 3.06s. One interrupted POST was saved and recovered by source-marker verification. |
| Full five-minute arrival, 50/min | 250 / 250 | 1,375 | All final outcomes and queue checks passed; **overall failed** the retained 3s receipt target at **3.085s**. Verdict p95 12.41s. |
| First participant journey | 750 estimated / 764 | 4,202 | Failed under clustered second-cycle traffic; details below. |
| Five-minute recovery, 12/min | 60 / 60 | 330 | Passed; receipt/verdict p95 2.29s / 12.69s. |
| 20-case fan-out plus correct/wrong MCQs | 3 / 3 | 20 | All expected outcomes passed; pooled verdict p95 17.22s across one coding and two MCQ submissions. |

An additional first baseline saved/finalized 10 submissions and 55 jobs but failed a harness boundary guard: k6 scheduled one excess iteration at the exact end of the arrival window. No extra POST was sent. The guard was fixed and the five-minute baseline repeated successfully. That run remains in the aggregate, without being called a platform failure or passing benchmark.

The strict receipt-only aborts led to an explicit diagnostic policy: **retain 3s as the pass condition; use 5s only as the hard receipt stop**. Correctness, authentication, read latency, verdict timeout, dropped-work and queue guards remained active. The full 50/min run still failed its 3s target; the corrected journey ultimately passed both targets. k6 threshold and arrival-executor behavior follow the [official threshold](https://grafana.com/docs/k6/latest/using-k6/thresholds/) and [VU-allocation](https://grafana.com/docs/k6/latest/using-k6/scenarios/concepts/arrival-rate-vu-allocation/) documentation.

The first journey exposed a harness pacing defect: initial submissions were staggered, but subsequent cycles partly realigned. Its busiest aligned 30-second bucket contained **135 submission starts**. At the 600-user stage, observed verdict p95 reached **63.70s** before abort; this is a partial observer sample, not the latency distribution of all 764 submissions. Four observers timed out and others were interrupted by the stop. Independent observation reconciled **all 764 as accepted**, including one interrupted, saved POST recovered without resubmission. Fleet queue depth peaked at **978**; oldest test-owned pending age reached **141.14s**, and drain upper bound was **131.42s**, failing the 120s recovery target. The pacing fix has a virtual-clock regression check and was followed by recovery verification and the full corrected repeat. This failed run is not representative average-load evidence, but its actual queue/timeout failure is retained. **The additional 10/s production deadline test was not attempted after that failure.**

## What still needs fixing

Callbacks work, but their processing is slow for multi-case submissions. The corrected journey logged **3,914 successful callback deliveries and 355 HTTP-500/context-canceled attempts around the Judge0 five-second timeout**; every execution eventually had a successful delivery. Even the single 20-case smoke logged 15 failed attempts before all 20 succeeded. Failures were confined to the multi-case fixtures in these audits. No signature-shaped values were found in the audited API, ingress, Judge0 web or worker logs.

`ExecutionStore.ProcessFinal` serializes callbacks on a parent submission and locks its execution rows while performing several database operations. This is a supported investigation target; actual per-query, pool-wait and lock-wait timings are not exported, so the precise database contribution is not quantified. Shorten the durable callback transaction and measure contention while preserving retry/idempotency and scoring guarantees. The current path also compiles C++/Java separately for each testcase. Evaluate the existing [compile-once candidate's separate functional validation](reports/compile-once-validation-20261107.json) in an isolated performance comparison; it was not deployed here and has no capacity certification. Compare worker/CPU changes only in isolation, one configuration at a time.

During the passing journey, host interval CPU peaked near 40%; worker-container samples reached 741% of one core on the eight-vCPU host. Available host memory stayed above 11.5GiB. No sampled container restart, OOM increase or API CPU throttling occurred. The separate generator peaked around **1.42 cores and 444.5MiB RSS**, with zero dropped iterations. It did not show evidence of being the bottleneck. Compilation duration, actual queue wait, API DB-pool waits and per-query latency remain unavailable; no missing duration was invented by subtracting clocks. `/workers` reports zero registrations despite real execution, so it is not used as a validated active-worker count.

## Fixtures, configuration and remaining limits

All scoring used two dedicated test contests and 603 managed synthetic identities. Before the corrected journey, one user/problem pair was populated with **one accepted submission followed by 40 wrong answers**; API pages 20/20/1 and retained accepted score were verified. These **41 submissions/41 jobs were outside timing**. Other users included empty and short histories; this is not 600 fully populated histories. Journey traffic included problem browsing, actual frontend-style 1s×1.6→5s status backoff/60s observation, details, history, leaderboard reads and optional admitted/rejected custom runs. The deployed problem-list response has no progress field, so frontend progress rendering is not certified; ranking totals and history were checked. Leaderboard pagination headers and test-user ownership were verified after the run.

The [current public site](https://recruitment.pointblank.club/) advertises **10 MCQs, five DSA problems and 120 minutes**. The public asset `/assets/index-BIelbj3X.js` was checked on 7 November. None of the ten existing contest configurations matched either 10/5/120 or 60/5/120. Confirm the actual recruitment contest and problem costs before extrapolating. The current public problem-list API method performs no per-problem history fan-out; the suite's optional extra fan-out is a conservative selector.

The workload assumption is 600 users×10 scored submissions/7,200s ≈0.833 submissions/s, with the regular fixture cycle averaging 5.5 testcases/submission. Proposed targets are read p95<2s, receipt p95<3s, verdict p95<60s, 100% expected outcomes, zero unresolved saved work/dropped iterations/auth/detail errors, and recovery≤120s. These are assumptions, not existing service SLOs. A separate constant-arrival 0.833/s run missed only the proposed receipt target; the independently drained constant-arrival profile passing every target was 0.2/s for five minutes. The passing mixed journey supplied the higher 0.83/s held-stage observation above.

**Not executed:** 120-minute soak, 10/s deadline period, 600-user synchronized spike, isolated stress/breakpoint, worker scalability comparisons, callback fault injection, Judge0 outages/worker interruption, direct-Judge0 isolation, large code/input/output volume, 600 multi-page histories, and the full real-contest MCQ/problem-cost distribution. A usable isolated environment was unavailable. The runnable profiles remain in [README.md](README.md); prepared or mocked tests are not real capacity results.

## Reconciliation, cleanup and reproduction

Across all twelve timed runs: **1,908 saved submissions and 10,475 testcase jobs**. Including untimed history provisioning: **1,949 saved submissions and 10,516 testcase jobs**, all reconciled to expected final outcomes. Two interrupted POSTs had saved work and were source-verified without retry; one other interrupted intent created no saved row after bounded observation and later reconciliation. No final work is unaccounted for. Across the two journeys, 290 custom-run attempts produced 31 successful executions and 259 expected 429s. The first journey also exercised 355 successful managed-token refreshes. Initial auth/fixture provisioning was outside measurement.

**Cleanup verified:** all 603 synthetic identities, both test contests and their dependent application rows, and all **1,975 recorded object keys** are absent. This includes 1,933 keys in the large pool and 42 in the small pool. Exact-ID checks found zero rows across users, contests, submissions, problems, registrations, rankings, executions, testcase results and per-user rate limits. Firebase identity absence and every recorded object key were independently checked. Candidate resources were untouched.

Native deletion is disabled, and SSH access remained read-only. **10,547 terminal native Judge0 records from this retest remain under existing retention**: 10,516 scored jobs and 31 custom executions. Their exact protected inventory remains in `perf/.private/`; earlier test cohorts are documented separately. The shared `run:capacity` limiter was left untouched.

Installed k6: **v2.3.0**, commit `e088784614`. All 19 profile/workload option checks, fixture Go tests, five local harness tests and the pacing regression passed. These local tests validate the harness, not judging capacity. Historical plans that mislabeled a work estimate as an upper bound are preserved; the final harness distinguishes the estimate from the budget-derived logical-job bound. No work cap was raised within a measurement.

For fresh fixtures, provisioning/cleanup and every profile command are in [README.md](README.md). The main measured selectors were:

```bash
# Full average-rate diagnostic: keeps the 3s pass condition.
WORKLOAD=arrival PARTICIPANTS=600 VUS=96 RATE_PER_MINUTE=50 DURATION_SECONDS=300 MAX_SUBMISSIONS=250 RECEIPT_ABORT_P95_MS=5000 python3 perf/run.py expected FIXTURE
# Recovery at 0.2 submissions/s, retaining the strict 3s receipt guard.
WORKLOAD=arrival PARTICIPANTS=600 VUS=96 RATE_PER_MINUTE=12 DURATION_SECONDS=300 MAX_SUBMISSIONS=60 python3 perf/run.py expected FIXTURE
# Before timing, on a fresh user/problem history:
perf/.private/fixtures seed-history FIXTURE
# Corrected participant journey, including populated-history reads.
PARTICIPANTS=600 POPULATED=1 ASSUMED_POLLS=3 MAX_SUBMISSIONS=1000 RECEIPT_ABORT_P95_MS=5000 python3 perf/run.py expected FIXTURE
# Fan-out and MCQ correctness, using fresh MCQ answers.
PARTICIPANTS=3 MATRIX=1 PROGRAM_OFFSET=9 MAX_SUBMISSIONS=3 python3 perf/run.py smoke FIXTURE
```

[Machine-readable retest results](results/callback-retest-20261107.json) contain plans, achieved work/rates, failed thresholds, stopped stages, callback audits, monitoring aggregates and cleanup verification. Raw IDs, credentials and source correlations remain protected in ignored local artifacts. The earlier [polling-only report](REPORT.md) remains historical evidence; the old single-account empty-history read measurements are not judging capacity certification.
