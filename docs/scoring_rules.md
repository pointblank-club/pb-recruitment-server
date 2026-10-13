# Contest Scoring & Final Ranking Consolidation Rules

This document specifies the official scoring, partial credit, penalty, tie-breaking, and ranking consolidation rules for the Point Blank recruitment platform.

---

## 1. Problem Scoring

### 1.1 Multiple Choice Questions (MCQs)
Each MCQ problem has a configured maximum score (`Score`), a set of options (`Options`), and an answer key (`Answer`) consisting of the 0-indexed indices of the correct options.

- **Points (Full Credit)**:
  - **Single-Choice Questions** (`len(Answer) == 1`):
    - A submission earns **100% of the problem score** if the selected choice strictly matches the answer key.
    - An incorrect selection earns **0 points**.
  - **Multiple-Choice Questions / Multiple Select Questions** (`len(Answer) > 1`):
    - A submission earns **100% of the problem score** if the candidate selects exactly all correct options and zero incorrect options.

- **Partial Credit Policy**:
  - **Strict Mode (Currently Enforced)**:
    - Multiple-select MCQs follow all-or-nothing evaluation. If any correct option is omitted or any incorrect option is selected, 0 points are awarded.
  - **Proportional Partial Credit**:
    - *Note*: Proportional partial credit is not currently implemented; only strict all-or-nothing evaluation exists. If implemented in future revisions:
      - If a candidate selects a strict subset of the correct options and **zero** incorrect options:
        $$\text{Score} = \left\lfloor \text{ProblemScore} \times \frac{|\text{Selected Correct}|}{|\text{Total Correct}|} \right\rfloor$$
      - If **any** incorrect option is chosen, the submission earns 0 points (strict penalty for incorrect guessing).

- **Penalties / Negative Marking**:
  - Incorrect MCQ submissions award 0 points by default. Total contest score for any candidate is bounded at a minimum of 0 (no negative contest totals).
  - Incorrect attempts do not deduct from already accumulated points from other problems, but count toward the total attempt penalty used in tie-breaking.

### 1.2 Coding Problems
Each coding problem has a configured score (`Score`) and a set of test cases evaluated via Judge0.

- **Points**:
  - A submission earns **100% of the problem score** if all test cases pass and the parent submission receives an `accepted` verdict.
  - Non-accepted verdicts (`wrong_answer`, `tle`, `rte`, `failed_to_process`, `judge_error`) earn **0 points**.

### 1.3 Per-Problem Solve Idempotency
- Candidates may submit multiple times for any problem (coding or MCQ).
- A problem is considered **solved** if the candidate has at least one `accepted` submission for it.
- A problem's score is awarded **at most once** per candidate regardless of how many accepted submissions are made.
- The **solve timestamp** for a problem is the `created_at` timestamp of the candidate's **earliest** accepted submission for that problem. Resubmissions after an accepted verdict do not push the solve timestamp later.

---

## 2. Total Score Computation
At contest end, a candidate's consolidated total score is:
$$\text{Total Score} = \sum_{p \in \text{Problems}} \text{Score}(p)$$
where $\text{Score}(p)$ is the score achieved by the candidate on problem $p$.

---

## 3. Tie-Breaking Hierarchy

When two or more candidates achieve the same total score, ties are broken strictly in the following priority order:

1. **Total Score (Descending)**:
   - Candidates with higher scores rank higher.
2. **Problems Solved Count (Descending)**:
   - Candidates who solved a greater number of distinct problems rank higher (e.g. 5 problems of 20 pts > 2 problems of 50 pts).
3. **Finish Time / Last Submission Time (Ascending)**:
   - The time of reaching the final score, defined as the maximum of per-problem earliest solve timestamps across all solved problems:
     $$\text{FinishTime} = \max_{p \in \text{SolvedProblems}} (\text{FirstSolveTime}(p))$$
   - Candidates who achieved their score earlier rank higher.
   - Candidates who solved $\ge 1$ problems rank ahead of candidates who solved 0 problems.
4. **Total Submission Attempts (Ascending)**:
   - Candidates with fewer submission attempts across the contest rank higher (penalizing brute-force attempts). Submissions with the final verdict `judge_error` are excluded: infrastructure failures must not penalize contestants.
   - Accepted submissions (including repeat solves) and genuine contestant failures (`wrong_answer`, `failed_to_process` for compilation errors, `tle`, `rte`, and `mle`) still count as attempts.
5. **Deterministic Ordering (User ID Ascending)**:
   - Ensures fully deterministic, reproducible ranking order.

---

## 4. Rank Assignment

- **Standard Competition Ranking**:
  - The highest-ranking participant receives Rank 1.
  - If two or more participants have identical Total Score, Problems Solved Count, Finish Time, and Total Attempts, they share the same rank.
  - The next candidate receives a rank equal to their 1-indexed position in the sorted list (e.g. `1, 2, 2, 4`).
- **Disqualified Participants**:
  - Candidates marked as `disqualified = true` are placed at the bottom of the rankings and assigned `rank = 0` (or excluded from the public leaderboard).
- **Hidden Participants**:
  - Candidates marked as `hidden = true` remain stored in `rankings` but are excluded from public leaderboard views.
- **Unranked / Rank-0 Participants**:
  - Participants with `rank = 0` (such as unranked entries created after consolidation or disqualified participants) sort to the bottom of the leaderboard after all officially ranked participants.

---

## 5. Contest Consolidator

The Consolidator is designed to run at contest conclusion:

1. **Grades Pending MCQs**: Evaluates any pending MCQ submissions against the answer key.
2. **Optional Re-grading**: Allows re-evaluating all MCQ submissions if answer keys were revised by contest authors.
3. **Combines Coding and MCQ Results**: Evaluates every registered participant and candidate who submitted solutions.
4. **Applies Tie-Breakers**: Sorts and assigns final ranks.
5. **Persists to Database**: Upserts final scores and ranks into the `rankings` table in a single set-based transaction.
6. **Idempotent**: Safe to run multiple times without score duplication or state corruption.

### Execution Mode & Lifecycle
- **Admin API Endpoint**: `POST /admin/contest/:id/consolidate` with optional `?regrade=true` and `?force=true`
- **Execution Gate**: Consolidation requires the contest to be closed (`GetRunningStatus() == closed`) and all pending code submissions to be evaluated (pending count = 0), returning HTTP 409 Conflict otherwise. Admins can pass `?force=true` to override this gate.
- **Leaderboard Integration**: Once a contest has been consolidated, the public leaderboard serves the official consolidated ranks, making all documented tie-breaks visible. If scores or visibility flags are modified after consolidation, a rerun is needed to refresh stored ranks.
- **Existing Final Ranks**: After deploying the `judge_error` attempt exclusion, an administrator must rerun consolidation for previously consolidated contests to refresh stored ranks. No schema migration is needed.

## 6. Contestant API Contract

- **Displayed Submission Counts**: The leaderboard's existing `problem_scores[].attempts` field counts all submissions for that problem, including pending submissions and `judge_error`. It is displayed as **Submissions** and is not the attempt penalty used for final ranking. Live ranks use score only; consolidated ranks apply the tie-breakers above.
- **Status**: `GET /submission/:id/status` returns only the submission verdict in `status`.
- **Details**: `GET /submission/:id/details` returns the verdict, per-testcase results, runtime and memory, along with submission metadata and the contestant's code.
- **Internal Diagnostics**: `compile_output` and `stderr` remain internal. Contestant responses must never include them, hidden testcase input/output, or Judge0 tokens.
