package stores

import (
	"app/internal/judge0"
	"app/internal/models"
	"context"
	"encoding/json"
	"fmt"
	"github.com/lib/pq"
)

// One short statement claims rows; no transaction remains open during network I/O.
func (s *ExecutionStore) ClaimRecovery(ctx context.Context, limit int, withToken bool) ([]models.Execution, error) {
	rows, err := s.db.QueryContext(ctx, `WITH due AS (
		SELECT e.id FROM submission_executions e
		WHERE e.status='pending' AND e.next_recovery_at<=now()
		AND (e.judge0_token IS NOT NULL)=$2
		AND EXISTS(SELECT 1 FROM submissions s WHERE s.id=e.submission_id AND s.status='pending')
		ORDER BY e.next_recovery_at,e.id LIMIT $1 FOR UPDATE SKIP LOCKED
	) UPDATE submission_executions e SET recovery_claim=gen_random_uuid()::text,
		next_recovery_at=now()+interval '30 seconds',
		recovery_failures=e.recovery_failures+1,
		dispatch_attempt=CASE WHEN e.judge0_token IS NULL THEN gen_random_uuid()::text ELSE e.dispatch_attempt END,
		dispatch_count=e.dispatch_count+CASE WHEN e.judge0_token IS NULL THEN 1 ELSE 0 END
	FROM due WHERE e.id=due.id
	RETURNING e.id::text,e.submission_id,e.test_case_index,COALESCE(e.judge0_token,''),e.dispatch_payload,
		e.dispatch_attempt,e.dispatch_count,e.recovery_failures,e.recovery_claim`, limit, withToken)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []models.Execution
	for rows.Next() {
		var e models.Execution
		var payload []byte
		if err := rows.Scan(&e.ID, &e.SubmissionID, &e.TestCaseIndex, &e.Judge0Token, &payload, &e.Attempt, &e.DispatchCount, &e.RecoveryFailures, &e.Claim); err != nil {
			return nil, err
		}
		e.DispatchPayload = payload
		items = append(items, e)
	}
	return items, rows.Err()
}

func (s *ExecutionStore) SaveAttemptToken(ctx context.Context, id, attempt, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE submission_executions SET judge0_token=$3,
		recovery_failures=CASE WHEN judge0_token IS NULL THEN 0 ELSE recovery_failures END
		WHERE id=$1 AND dispatch_attempt=$2 AND status='pending' AND (judge0_token IS NULL OR judge0_token=$3)`, id, attempt, token)
	return err
}

// Invalidate the refused attempt so duplicate releases cannot refund it twice.
func (s *ExecutionStore) DeferDispatch(ctx context.Context, e models.Execution) error {
	_, err := s.db.ExecContext(ctx, `UPDATE submission_executions
		SET dispatch_count=dispatch_count-1,dispatch_attempt=gen_random_uuid()::text,recovery_claim='',
		recovery_failures=GREATEST(0,recovery_failures-CASE WHEN $3='' THEN 0 ELSE 1 END),
		next_recovery_at=now()+interval '1 second'
		WHERE id=$1 AND dispatch_attempt=$2 AND ($3='' OR recovery_claim=$3)
		AND status='pending' AND judge0_token IS NULL AND dispatch_count>0`, e.ID, e.Attempt, e.Claim)
	return err
}

func (s *ExecutionStore) SaveRecoveryPayload(ctx context.Context, e models.Execution, job judge0.SubmissionRequest) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE submission_executions SET dispatch_payload=$3
		WHERE id=$1 AND recovery_claim=$2 AND status='pending'`, e.ID, e.Claim, data)
	return err
}

func (s *ExecutionStore) ReleaseRecovery(ctx context.Context, items []models.Execution, healthy []bool) error {
	if len(items) != len(healthy) {
		return fmt.Errorf("recovery release outcome count mismatch")
	}
	if len(items) == 0 {
		return nil
	}
	ids, claims := make([]string, len(items)), make([]string, len(items))
	for i, e := range items {
		ids[i], claims[i] = e.ID, e.Claim
	}
	_, err := s.db.ExecContext(ctx, `WITH current AS (
		SELECT e.id,outcome.healthy FROM unnest($1::uuid[],$2::text[],$3::boolean[]) AS outcome(id,claim,healthy)
		JOIN submission_executions e ON e.id=outcome.id
		WHERE e.recovery_claim=outcome.claim AND e.status='pending'
		ORDER BY e.id FOR UPDATE OF e
	) UPDATE submission_executions e SET next_recovery_at=now()+interval '15 seconds' * LEAST(8, CASE WHEN judge0_token IS NULL THEN GREATEST(2,recovery_failures) WHEN current.healthy THEN 1 ELSE recovery_failures END),
		recovery_failures=CASE WHEN current.healthy THEN 0 ELSE recovery_failures END
		FROM current WHERE e.id=current.id`, pq.Array(ids), pq.Array(claims), pq.Array(healthy))
	return err
}
