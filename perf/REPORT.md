# Point Blank judging performance report — 6 November 2026

Later results: [the callback retest](CALLBACK-RETEST-20261107.md) includes the real 600-user journey, arrival tests, failures and cleanup. [Signed callbacks were enabled and verified](CALLBACKS.md) before that retest. The measurements and callback-disabled configuration below describe the earlier polling-only test window.

**500–600-participant readiness is not demonstrated.** A five-minute, low-rate real-judge baseline passed at **2 offered scored submissions/minute**. A separate three-user submission smoke failed the **proposed** 60-second final-verdict p95 threshold. The previous 600-concurrent empty-history read measurements are not judging capacity evidence.

## What actually ran

All scoring used new synthetic Firebase accounts registered in dedicated `pbperf-*` contests. The load generator ran on the workstation, separate from the judge host. No existing recruitment contest, candidate submission or ranking was modified. No application behavior, deployment, worker count, admission limit or security setting was changed.

| Run | Planned / saved submissions | Final logical testcase jobs | Verdict p95 / maximum | Outcome |
| --- | --- | --- | --- | --- |
| Initial smoke `smoke-01` | 12 / 3 | 12 / 12 | k6 observers interrupted | **Harness failure:** one dropped iteration with only three allocated VUs; aborted after ~33s. Independent observation finalized all three as accepted. |
| Corrected smoke `smoke-02` | 3 / 3 | 12 / 12 | **63.11s / 63.63s** | **Failed proposed 60s p95 threshold.** All three accepted, no lost work, zero dropped iterations and zero score mismatches. |
| Low baseline `baseline-low` | 10 / 10 | 55 / 55 | **53.94s** | **Passed.** 300s arrival schedule; ~327.5s generator/observation process and ~330.8s full reconciliation window. |
| Individual correctness smokes | one each | one each for WA/CE/RTE/TLE/memory | 33.33s, 58.63s, 38.08s, 58.97s, 59.03s respectively | All expected verdicts passed. Wrong/failed programs are successful correctness checks. |
| MCQ correct / wrong | 1 / 1 each | zero judge jobs | 3.43s / 3.08s | Expected accepted/wrong-answer feedback verified through status and details. |
| Custom-run admission smoke | zero scored submissions | 3 completed custom executions | synchronous run endpoint measured separately | Six bounded calls: three successful executions and three expected 429s. |
| Final single-user harness smoke | 1 / 1 | 1 / 1 | 43.15s | Passed with frontend-style base64 submission and a run rejected while scored work was pending. |
| Final fixture publisher check | 1 / 1 | 1 / 1 | 38.20s | All six authenticated problem statements/sample answers verified; new deterministic fixture publisher then passed real judging. |

The real baseline used Python, C++ and Java, plus moderately expensive correct Python, with 1- and 10-testcase problems. Its metrics were:

- Receipt p95 **1.966s**; read p95 **0.444s**; final-verdict p95 **53.944s**.
- 10 submission POSTs, 10 saved IDs, 10 expected accepted final verdicts, 55 persisted testcase results. There were 115 status polls (11.5/submission), ten detail reads, and one preflight read: 136 HTTP requests. Future traffic planning now uses 12 polls/submission rather than the initial six-poll assumption.
- Zero dropped iterations, scored admission rejections, unexpected verdicts, unresolved submissions or score mismatches.
- Greatest sampled test-owned pending age **49.26s**; backlog cleared within a sampled upper bound of **56.14s after the last receipt**.
- Offered rate **0.03333 scored submissions/s** over the five-minute schedule. Achieved completion rate including final observation/reconciliation **0.03023 submissions/s** and **0.16625 logical testcase completions/s**.

This is the **highest sustained rate demonstrated here under passing conditions**, not a maximum capacity finding. It used three distinct identities and ten submissions. At the user's example workload, 600 × 10 / 7,200 = **0.8333 scored submissions/s**, 25 times the demonstrated offered rate. Neither that average nor the separate 10/s deadline hypothesis was tested.

Across 13 executed measurement runs: **25 saved submissions reconciled, 86 scored testcase jobs finalized, five custom executions completed, and no saved work remained pending.** Nine custom-run calls received expected admission rejections across the diagnostics; they were excluded from completed judge throughput. The first aborted smoke's completion-rate report was corrected to include post-abort observation in its denominator. It is never treated as a passing capacity result.

## Judging path and contracts verified

Backend/local/deployed revision: `2efc783c2e87a407a4ac922e103b7d2b150491f1`; local frontend revision: `8d2a40d49266b1f81daf3eead5b88acb66800703`.

1. Firebase bearer identity is checked by the auth middleware. `POST /contests/:id/registration` takes `{"action":"register"}`; registration window and student-year eligibility apply. Timed runs use pre-provisioned, pre-registered synthetic identities.
2. `POST /submission/submit` takes `contest_id`, `problem_id`, `type`, and either `language`/`code` or MCQ `option:[index]`. Code is base64 in the current frontend; the backend also accepts plain source. **201 is receipt only**, with `submission_id`.
3. Code/testcases/language are validated and the per-user submission cooldown is checked. The submission is saved, source is written to object storage, and one persisted execution/payload is created per testcase. Judge0 receives batched work, up to 20 jobs per batch. Each testcase invokes its own compile/execute job.
4. The callback route is signed `PUT /internal/judge0/callback/:execution_id`. The deployed file has **no callback secret configured**, so callbacks are disabled and recovery polling delivers results. Recovery sweeps run every 15s, claim token and tokenless work separately, poll batched results, defer queue-full dispatch, and retry bounded failures. An ambiguous dispatch can cause duplicate physical CPU work; dispatch-attempt counts were zero above the first attempt in these tests.
5. Finalization records one result per execution and waits for every testcase before aggregating the parent verdict. Ranking is recalculated from problems with **any accepted submission**, under transaction/row locks. Later failed submissions do not remove an earlier accepted score. Final reconciliation found no score mismatch.
6. `GET /submission/:id/status` returns `{"status":"..."}` for its owner. The frontend waits 1s, multiplies poll delay by 1.6 (rounded in milliseconds), caps at 5s, stops after roughly 60s or three consecutive read failures, and fetches details on a final result. Its last request can finish beyond the nominal timeout boundary. The harness follows that behavior and measures start-to-observed-final and receipt-to-observed-final separately.
7. `GET /submission/:id/details` returns the stored submission/source and per-case results; `GET /submission/list?problem_id=...&page=0` returns `{"submissions":[...]}`, 20 items/page, newest first. Ownership and response identity/results are checked. The suite includes separate real-history seeding of an old accepted solution plus 40 later WAs, but that 41-item scenario was **not executed**.
8. `POST /submission/run` uses plain UTF-8 `language`, `code`, `stdin`; max code/stdin 64KiB, request body 1MiB, and output fields 4096 bytes. It polls Judge0 for up to 15s; the frontend allows 20s. A successful execution is not a problem-test pass. Its token is not returned, so timeout cannot be cancelled/retried safely. The suite never automatically retries it.

Deployed `ENABLE_RUN=true` was verified. Source and observed 429 behavior agree with: scored-submit cooldown **5s/user**, custom runs **5s/user**, at most **one fleet admission per database-clock second**, and rejection while scored code is pending or Judge0 refuses a full queue. A run rejected for these reasons has the same message/Retry-After=5; the harness deliberately does not claim to distinguish the underlying 429 reason.

The current API maps Judge0 compilation errors to `failed_to_process`, CPU timeout to `tle`, and sandbox memory exhaustion to `rte`; it has no separate deployed MLE mapping. The bounded 96MiB allocation under a 32MiB fixture limit returned expected `rte`.

## Configuration and topology

The public frontend asset `index-DfCBBD_n.js` currently advertises **10 MCQs, five DSA problems and 120 minutes**, rather than 60 MCQs. The ten pre-existing database contest configurations had zero to three coding problems and zero to two MCQs; none matched the advertised 10/5/120 structure. An actual upcoming recruitment configuration was therefore not established.

The current local/deployed frontend fetches history when a problem is opened. Its `ProblemCard` list component performs no history request. `HISTORY_FANOUT=1` is an explicit conservative additional workload, not a description of the verified default frontend behavior. The participant journey models coding activity; a complete advertised MCQ-answer traffic distribution remains unmodeled.

The documented public route is HTTPS ingress/Caddy → Go API on host localhost:8080 → Judge0 on localhost:2358. Read-only Docker inspection found one API, Caddy, a retained temporary HTTPS tunnel, Judge0 web/workers, Redis and PostgreSQL. The API uses a managed external PostgreSQL/object-store configuration; existing infrastructure documentation labels it development, and the process has `STAGE=e2e`. Despite those labels, it serves the public API and shares the judge host, so it is **not an isolated fault/stress environment**.

Verified host: eight vCPUs, ~15.34GiB RAM, no swap. The API container has a one-CPU/1GiB limit. Judge0 web/workers have no Docker CPU/memory cap; the Judge0 queue limit is 1,000 and max batch size 20. The configured API DB pool is max 25, idle 5. No worker or resource setting changed.

## Supported bottleneck conclusions

Timestamped samples showed **zero Judge0 queued/running jobs while the API still had test-owned pending executions/submissions**. For example, the initial smoke's Judge0 pending sample was zero at 14:15:29 UTC while the API still reported three pending parents; all API parents were reconciled by the 14:16:07 sample. Native inventory later matched all scored Judge0 tokens and confirmed terminal states. This supports investigating result recovery/aggregation/storage delivery before attributing the observed end-to-end delay solely to code execution. It does not prove which database query or recovery operation dominates.

During the baseline, k6 peak sampled RSS was **44.82MiB**, CPU below **1% of one core**, 11 open file descriptors, and no dropped iterations. The generator was adequate for this low-rate run; its ability to generate the 600-user profiles is untested.

Host average CPU over sample intervals peaked near **8%** during baseline and **10.2%** in corrected smoke. Short worker-container samples reached **339%** and **738% of one core**, respectively, so compilation bursts can use several cores even while longer intervals are quiet. API memory was about **20.2MiB/1GiB**, worker memory about **1.424GiB**, no swap use, and sampled container restarts/OOM state did not increase. These observations do not establish scaling headroom.

The API exports no Go DB-pool in-use/wait statistics or per-query timings. Actual queue wait, compilation duration and callback processing duration were not available. PostgreSQL block-time counters were zero without verified IO timing instrumentation, so they are not reported as zero-latency IO. `/workers` reports zero registered workers even while code executes; that field is not a validated active-worker count. Stored Judge0 runtime is execution time only, summed across testcase jobs; no missing timing was invented by subtracting unrelated clocks.

## Prepared but not executed

- 50/100/300/500/600-user journey stages, normal 0.833/s arrival profile, 10/s deadline period and 600-user synchronized burst. Production load was not increased to these levels after the three-user latency threshold failure.
- 120-minute soak, token refresh under load, 20-testcase scored fan-out, 41-item histories, large code/input volume and the full mixed/fairness profile.
- Isolated stress, sustained breakpoint staircase, worker-configuration comparisons, delayed/missing/duplicate real callbacks, transient Judge0 failures, worker interruption, and direct-Judge0 isolation testing. The local Docker Desktop socket was unavailable; the public host was not used as a substitute isolated environment.

All profile/workload options were checked by the installed k6 binary without network execution. Local unit tests exercised proxy forwarding/delay/drop/duplicate/transient modes and rejection of a foreign worker name; those are **harness tests, not real Judge0 resilience results**. The repository's `judge0_load_test.go` uses `httptest` Judge0 responses and cannot certify execution throughput. Its `judge0_live*` tests do execute real code but require explicit disposable DB/live Judge0 inputs and were not run here against the shared production judge.

No throughput, soak, breakpoint, resilience or 600-identity success is claimed from prepared scripts.

## Cleanup and evidence

Four test-run manifests cover the initial unused fixture pool, the main measurement pool, a failed fixture publication detected by verification, and the successful final publisher check. Cleanup was exercised on both complete and partial provisioning. Verification checked the exact test-owned rows, each Firebase identity and every recorded object key: **zero remaining application rows/users/objects; 67 object keys verified absent**. Eight synthetic identities across the four pools were removed. No candidate resources were cleaned.

**91 native Judge0 execution records remain under its existing retention:** 86 scored jobs and five completed custom runs. All are terminal. Judge0 submission deletion is disabled, so no setting was changed to remove them. The main archive includes 85 exact scored-token matches plus five custom matches from known source/stdin and bounded successful request windows; those earliest custom runs lacked a source run-ID marker. This correlation was used only for inventory, never deletion. The final harness marks every new custom program with its run ID. The separate publisher archive contains its one scored job. Shared Redis/service state and the fleet `run:capacity` row were left untouched.

- [Runnable commands, assumptions and official k6 references](README.md)
- [Machine-readable aggregate results](results/results-index.json)
- [Validated k6 options inventory](results/validation.json)
- Protected raw artifacts: `perf/results/<label>/{plan,summary,report}.json`, `metrics.jsonl`, `correlation.jsonl`, `host.jsonl`, `generator.jsonl`, `backlog.jsonl`, and `reconciliation.json`.
- Protected fixture/auth/resource manifests and archived native Judge0 inventory: `perf/.private/`. Raw directories are ignored by Git and must not be published.

Installed k6: **v2.3.0**, commit `e088784614`, Go `1.26.8`, linux/amd64. Official archive SHA256 was verified: `39c3117b6af817592dcd0ce4242105c0a7af10948c2a425306f0be8f7a8a8ab1`. No application source files changed.
