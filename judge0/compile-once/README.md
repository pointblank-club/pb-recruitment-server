# Compile once, keep separate testcase sandboxes

Draft implementation for Judge0 CE 1.13.1. **Production remains unchanged.**

The recruitment API currently submits one Judge0 job per testcase, so C++/Java
compile repeatedly. A multi-file `run` loop would put the harness and all cases
inside the candidate's sandbox, sharing its limits and writable filesystem. This
implementation retains the existing native Judge0 job, isolate invocation,
limits, output comparison, callback and recovery behavior for every testcase.

With `JUDGE0_COMPILE_ONCE=true`, the backend includes the parent submission UUID
as `compilation_group` for C++ (54) and Java (62). It is persisted in each dispatch
snapshot, so recovery keeps the same group. Custom runs, legacy snapshots and
other languages continue unchanged. The public recruitment API is unchanged;
`compilation_group` extends the private Judge0 API.

The patched worker compiles in the ordinary compile sandbox. Before any candidate
execution, it snapshots the compile directory into PostgreSQL. Each later case
restores a fresh copy into its own isolate directory. Test input, expected output,
stdout and judge metadata live outside that directory and are never cached.
Candidates cannot modify the stored artifact or another testcase's copy.

A transaction-scoped PostgreSQL advisory lock serializes compilation for the
same group across worker processes/hosts. A fingerprint covers source, language,
compiler/run commands, compiler options, compile limits, locale and operator
supplied compiler-build identity; a conflicting group fails closed. Compilation
errors are also shared, with the original error verdict/diagnostics. Failed cache
transactions roll back; a worker crash may require recompilation, as with ordinary
dispatch recovery. This is not an exactly-once claim across failures.

Regular artifacts are bounded to 8 MiB and 256 directory entries. Symlinks,
nonregular/unreadable/oversized artifacts use ordinary compilation rather than
changing the candidate verdict. Cache restore rejects traversal, duplicate and
conflicting paths. Projects/additional files are unsupported for groups. Expiry
removes up to 100 entries older than 24 hours per invocation, retaining entries
referenced by queued/running native jobs. Native submissions/results are untouched.

## Coordinated rollout (not performed)

1. Use a pinned Judge0 1.13.1 base image digest. Build this overlay with a nonempty
   `COMPILER_BUILD_ID` identifying the exact base/toolchain/configuration. Use the
   same identity on all workers; change it whenever the compiler environment changes.
2. Back up Judge0's database, then apply the bundled migration through the API
   image's normal migration startup. This migration targets **Judge0 PostgreSQL**,
   not the recruitment database. Keep backend compilation groups disabled.
3. Deploy the patched image to the Judge0 API and **every** worker. Preserve the
   existing callback-log-redaction initializer/Compose override and all sandbox,
   resource, networking, authentication and admission settings.
4. Perform the isolated validation below, then deploy the backend and enable
   `JUDGE0_COMPILE_ONCE=true`. Stock Judge0 ignores unknown fields, so backend-first
   rollout would silently retain repeated compilation.
5. To roll back, disable the backend flag first. Drain queued grouped work before
   removing the worker overlay or reversing the migration. Do not drop artifacts
   or the group column while grouped jobs are pending.

Example build for a future approved rollout:

```bash
docker build --build-arg JUDGE0_BASE_IMAGE=judge0/judge0@sha256:REVIEWED_DIGEST \
  --build-arg COMPILER_BUILD_ID=REVIEWED_COMPILER_BUILD_ID \
  -t recruitment-judge0:compile-once judge0/compile-once
```

## Validation required before merge/deployment

The later request to test supersedes the original request to skip tests. Local
Go tests passed with race detection and disposable PostgreSQL; artifact tests
passed; cache integration tests passed with ten concurrent cases, real C++/Java
compilation and distinct inputs, compilation failure reuse, rollback, fingerprint
mismatch and expiry across time zones. The expiry test exposed a timezone
assumption; cache timestamp writes/comparisons now explicitly use UTC.

The `Compile-once Judge0 integration` workflow builds the overlay from a resolved
1.13.1 image digest and tests the migration, native Rails request contract and
shared compilation against disposable PostgreSQL. No production settings or data
are used. This is not a native isolate end-to-end test or a load/capacity test;
those remain required before enabling the feature.

- Run Go tests including compilation-group JSON/recovery snapshot compatibility.
- Run the standalone Ruby artifact tests: `ruby test/compilation_cache_test.rb`
  from this overlay directory (or `/api` in the patched image).
- In disposable Judge0 PostgreSQL, check simultaneous same-group jobs across two
  workers compile once; source/options/toolchain mismatches never reuse artifacts;
  a killed compiler releases its transaction lock; CE is propagated to every case;
  expired entries referenced by pending jobs survive housekeeping.
- Verify C++ and Java accepted, WA, CE, RTE, TLE, memory/output limits and blank
  output against independent cases; candidate mutations must not reach another
  case. Reconcile callbacks, retries, final results and score awards.
- Compare compilation counts, CPU and end-to-end latency under identical traffic.
  Ten testcase jobs remain ten executions; the optimization removes nine compiler
  invocations, not nine sandbox runs or callbacks. Overall savings are unmeasured.

Primary references:

- [Judge0 multi-file API](https://ce.judge0.com/#submissions-submission)
- [Pinned 1.13.1 worker compile/run lifecycle](https://github.com/judge0/judge0/blob/v1.13.1/app/jobs/isolate_job.rb)
