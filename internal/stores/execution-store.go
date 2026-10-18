package stores

import (
	"app/internal/judge0"
	"app/internal/models"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type FinalExecutionResult struct {
	Stdout                     json.RawMessage
	Attempt, Claim             string
	ExecutionID, Token, Status string
	Runtime, Memory            int64
	CompileOutput, Stderr      string
}
type executionChild struct {
	id, status string
}

type ExecutionStore struct {
	db *sql.DB
}

func NewExecutionStore(db *sql.DB) *ExecutionStore {
	return &ExecutionStore{db: db}
}

func (s *ExecutionStore) InsertBatch(ctx context.Context, submissionID string, indexes []int, jobs ...[]judge0.SubmissionRequest) ([]models.Execution, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("execution store: db is not initialized")
	}
	if len(indexes) == 0 {
		return []models.Execution{}, nil
	}

	now := time.Now().Unix()
	executions := make([]models.Execution, len(indexes))
	ids := make([]string, len(indexes))
	payloads, attempts := make([]string, len(indexes)), make([]string, len(indexes))
	if len(jobs) > 0 && len(jobs[0]) != len(indexes) {
		return nil, fmt.Errorf("execution job count mismatch")
	}

	for i, idx := range indexes {
		id := uuid.NewString()
		ids[i] = id
		payloads[i] = "null"
		if len(jobs) > 0 {
			payload, err := json.Marshal(jobs[0][i])
			if err != nil {
				return nil, err
			}
			payloads[i], attempts[i] = string(payload), uuid.NewString()
		}
		executions[i] = models.Execution{
			ID:            id,
			SubmissionID:  submissionID,
			TestCaseIndex: idx,
			Attempt:       attempts[i],
		}
	}

	const q = `
		INSERT INTO submission_executions (id, submission_id, test_case_index, status, created_at, dispatch_payload, dispatch_attempt, dispatch_count, next_recovery_at)
		SELECT id, $2, idx, 'pending', $4, convert_to(NULLIF(payload,'null'),'UTF8'), attempt,
		CASE WHEN attempt='' THEN 0 ELSE 1 END, now()+interval '30 seconds'
		FROM unnest($1::uuid[],$3::int[],$5::text[],$6::text[]) AS x(id,idx,payload,attempt)
	`

	_, err := s.db.ExecContext(ctx, q, pq.Array(ids), submissionID, pq.Array(indexes), now, pq.Array(payloads), pq.Array(attempts))
	if err != nil {
		return nil, fmt.Errorf("insert executions: %w", err)
	}

	return executions, nil
}

func (s *ExecutionStore) ProcessFinal(ctx context.Context, r FinalExecutionResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var subID, userID, contestID, problemID, subStatus string
	if err = tx.QueryRowContext(ctx, `SELECT s.id,s.user_id,s.contest_id,s.problem_id,s.status FROM submissions s JOIN submission_executions e ON e.submission_id=s.id WHERE e.id=$1 FOR UPDATE OF s`, r.ExecutionID).Scan(&subID, &userID, &contestID, &problemID, &subStatus); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,status FROM submission_executions WHERE submission_id=$1 ORDER BY id FOR UPDATE`, subID)
	if err != nil {
		return err
	}
	var children []executionChild
	for rows.Next() {
		var c executionChild
		if err = rows.Scan(&c.id, &c.status); err != nil {
			rows.Close()
			return err
		}
		children = append(children, c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var currentStatus, currentToken, attempt, claim string
	var index int
	var storedRuntime, storedMemory int64
	var payload []byte
	if err = tx.QueryRowContext(ctx, `SELECT status,COALESCE(judge0_token,''),test_case_index,COALESCE(runtime,0),COALESCE(memory,0),dispatch_attempt,recovery_claim,dispatch_payload FROM submission_executions WHERE id=$1`, r.ExecutionID).Scan(&currentStatus, &currentToken, &index, &storedRuntime, &storedMemory, &attempt, &claim, &payload); err != nil {
		return err
	}
	// Old callbacks and expired sweepers cannot finalize a newer attempt.
	if currentStatus == "pending" && (r.Attempt != attempt || (r.Claim != "" && r.Claim != claim)) {
		return tx.Commit()
	}
	if currentToken != "" && r.Token != "" && currentToken != r.Token {
		// Acknowledge stale deliveries after completion without changing the stored result.
		if currentStatus != "pending" {
			return tx.Commit()
		}
		return fmt.Errorf("conflicting Judge0 token")
	}
	alreadyTerminal := currentStatus != "pending"
	if !alreadyTerminal && currentToken == "" && r.Token != "" {
		_, err = tx.ExecContext(ctx, `UPDATE submission_executions SET judge0_token=$2,recovery_failures=0 WHERE id=$1 AND judge0_token IS NULL`, r.ExecutionID, r.Token)
		if err != nil {
			return err
		}
	}
	if !alreadyTerminal && r.Status == "wrong_answer" && bytes.Equal(bytes.TrimSpace(r.Stdout), []byte("null")) {
		// Legacy callbacks defer grading until recovery loads the missing expected output.
		if len(payload) == 0 {
			return tx.Commit()
		}
		// Judge0 1.13.1 compares null stdout to blank text; match its ASCII rstrip semantics.
		var job judge0.SubmissionRequest
		if json.Unmarshal(payload, &job) == nil && job.ExpectedOutput != nil && strings.Trim(*job.ExpectedOutput, " \t\n\r\v\f\x00") == "" {
			r.Status = "accepted"
		}
	}
	if alreadyTerminal {
		r.Status = currentStatus
		r.Runtime, r.Memory = storedRuntime, storedMemory
	}
	if !alreadyTerminal {
		_, err = tx.ExecContext(ctx, `UPDATE submission_executions SET status=$2,runtime=$3,memory=$4,compile_output=$5,stderr=$6 WHERE id=$1 AND status='pending'`, r.ExecutionID, r.Status, r.Runtime, r.Memory, r.CompileOutput, r.Stderr)
		if err != nil {
			return err
		}
		for i := range children {
			if children[i].id == r.ExecutionID {
				children[i].status = r.Status
			}
		}
	}
	conflict := `ON CONFLICT(execution_id) DO UPDATE SET status=EXCLUDED.status,runtime=EXCLUDED.runtime,memory=EXCLUDED.memory`
	if alreadyTerminal {
		conflict = `ON CONFLICT(execution_id) DO NOTHING`
	}

	tcStatus := r.Status
	if tcStatus == "accepted" {
		tcStatus = "pass"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO test_case_results(id,execution_id,submission_id,test_case_id,status,runtime,memory,created_at) VALUES($1,$1,$2,$3,$4,$5,$6,$7) `+conflict, r.ExecutionID, subID, fmt.Sprint(index), tcStatus, r.Runtime, r.Memory, time.Now().Unix())
	if err != nil {
		return err
	}
	if subStatus != "pending" {
		return tx.Commit()
	}
	var pending, accepted bool
	var totalRuntime, totalMemory int64
	err = tx.QueryRowContext(ctx, `SELECT bool_or(status='pending'),bool_and(status='accepted'),COALESCE(sum(runtime),0),COALESCE(max(memory),0) FROM submission_executions WHERE submission_id=$1`, subID).Scan(&pending, &accepted, &totalRuntime, &totalMemory)
	if err != nil {
		return err
	}
	if !pending {
		// Reconciliation must restore every missing result before completing the parent.
		_, err = tx.ExecContext(ctx, `INSERT INTO test_case_results(id,execution_id,submission_id,test_case_id,status,runtime,memory,created_at)
			SELECT id,id,submission_id,test_case_index::text,
			(CASE WHEN status='accepted' THEN 'pass' ELSE status END)::test_case_status,
			COALESCE(runtime,0),COALESCE(memory,0),$2
			FROM submission_executions WHERE submission_id=$1 AND status<>'pending'
			ON CONFLICT(execution_id) DO NOTHING`, subID, time.Now().Unix())
		if err != nil {
			return err
		}
		verdict := aggregateFailure(children)
		if accepted {
			verdict = "accepted"
		}
		_, err = tx.ExecContext(ctx, `UPDATE submissions SET status=$2,runtime=$3,memory=$4 WHERE id=$1 AND status='pending'`, subID, verdict, totalRuntime, totalMemory)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO rankings(contest_id,user_id,score,hidden,disqualified) VALUES($1,$2,0,false,false) ON CONFLICT(contest_id,user_id) DO NOTHING`, contestID, userID)
		if err != nil {
			return err
		}
		var rankID string
		err = tx.QueryRowContext(ctx, `SELECT user_id FROM rankings WHERE contest_id=$1 AND user_id=$2 FOR UPDATE`, contestID, userID).Scan(&rankID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE rankings SET score=COALESCE((SELECT sum(p.score) FROM problems p WHERE p.contest_id=$1 AND EXISTS(SELECT 1 FROM submissions x WHERE x.contest_id=p.contest_id AND x.problem_id=p.id AND x.user_id=$2 AND x.status='accepted')),0) WHERE contest_id=$1 AND user_id=$2`, contestID, userID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func aggregateFailure(children []executionChild) string {
	// Precedence: compilation, wrong answer, time limit, runtime, judge error.
	for _, want := range []string{"failed_to_process", "wrong_answer", "tle", "rte", "judge_error"} {
		for _, c := range children {
			if c.status == want {
				return want
			}
		}
	}
	return "judge_error"
}

func (s *ExecutionStore) TerminalPendingParents(ctx context.Context, limit int) ([]FinalExecutionResult, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT ON (s.id) e.id::text,e.status,COALESCE(e.runtime,0),COALESCE(e.memory,0)
		FROM submission_executions e JOIN submissions s ON s.id=e.submission_id
		WHERE s.status='pending' AND e.status<>'pending'
		AND NOT EXISTS (SELECT 1 FROM submission_executions pending WHERE pending.submission_id=s.id AND pending.status='pending')
		ORDER BY s.id,e.created_at,e.id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FinalExecutionResult{}
	for rows.Next() {
		var r FinalExecutionResult
		if err = rows.Scan(&r.ExecutionID, &r.Status, &r.Runtime, &r.Memory); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
