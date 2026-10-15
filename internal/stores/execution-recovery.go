package stores

import (
	"app/internal/judge0"
	"app/internal/models"
	"context"
	"encoding/json"
)

// One short statement claims rows; no transaction remains open during network I/O.
func (s *ExecutionStore) ClaimRecovery(ctx context.Context, limit int) ([]models.Execution, error) {
	rows, err := s.db.QueryContext(ctx, `WITH due AS (
		SELECT e.id FROM submission_executions e
		WHERE e.status='pending' AND e.next_recovery_at<=now() AND e.claim_until<=now()
		AND EXISTS(SELECT 1 FROM submissions s WHERE s.id=e.submission_id AND s.status='pending')
		ORDER BY e.next_recovery_at,e.id LIMIT $1 FOR UPDATE SKIP LOCKED
	) UPDATE submission_executions e SET recovery_claim=gen_random_uuid()::text,
		claim_until=now()+interval '30 seconds', next_recovery_at=now()+interval '30 seconds',
		recovery_failures=e.recovery_failures+1,
		dispatch_attempt=CASE WHEN e.judge0_token IS NULL THEN gen_random_uuid()::text ELSE e.dispatch_attempt END,
		dispatch_count=e.dispatch_count+CASE WHEN e.judge0_token IS NULL THEN 1 ELSE 0 END
	FROM due WHERE e.id=due.id
	RETURNING e.id::text,e.submission_id,e.test_case_index,COALESCE(e.judge0_token,''),e.dispatch_payload,
		e.dispatch_attempt,e.dispatch_count,e.recovery_failures,e.recovery_claim`, limit)
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
	_, err := s.db.ExecContext(ctx, `UPDATE submission_executions SET judge0_token=$3
		WHERE id=$1 AND dispatch_attempt=$2 AND status='pending' AND (judge0_token IS NULL OR judge0_token=$3)`, id, attempt, token)
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

func (s *ExecutionStore) ReleaseRecovery(ctx context.Context, e models.Execution, healthy bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE submission_executions SET claim_until='-infinity',
		next_recovery_at=now()+interval '15 seconds' * LEAST(8, CASE WHEN $3 THEN 1 ELSE recovery_failures END),
		recovery_failures=CASE WHEN $3 THEN 0 ELSE recovery_failures END
		WHERE id=$1 AND recovery_claim=$2 AND status='pending'`, e.ID, e.Claim, healthy)
	return err
}
