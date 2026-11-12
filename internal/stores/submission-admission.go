package stores

import (
	"app/internal/common"
	"app/internal/models"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *SubmissionStore) FindCodeSubmissionRequest(ctx context.Context, userID, requestID, fingerprint string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("submission database unavailable")
	}
	var id, stored string
	err := s.db.QueryRowContext(ctx, `SELECT submission_id,request_hash FROM code_submission_requests WHERE user_id=$1 AND request_id=$2`, userID, requestID).Scan(&id, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if stored != fingerprint {
		return "", common.ErrIdempotencyConflict
	}
	return id, nil
}

// Reserve a durable parent before object reads; the recovery worker handles interrupted preparation.
func (s *SubmissionStore) CreateCodeSubmission(ctx context.Context, sub *models.Submission, requestID, fingerprint string, cooldown int64, maxPending int) (id string, replay bool, err error) {
	defer func() {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "55P03" || (pgErr.Code == "23505" && pgErr.ConstraintName == "idx_pending_code_slot")) {
			err = &common.SubmissionCooldownError{RetryAfter: 1}
		}
	}()
	if s == nil || s.db == nil || maxPending < 1 || cooldown < 1 {
		return "", false, fmt.Errorf("invalid submission admission configuration")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	// Bound same-user/registration waits without serializing unrelated participants.
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout='500ms'`); err != nil {
		return "", false, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('code:user:' || $1,0))`, sub.UserID); err != nil {
		return "", false, err
	}
	if requestID != "" {
		var id, stored string
		err = tx.QueryRowContext(ctx, `SELECT submission_id,request_hash FROM code_submission_requests WHERE user_id=$1 AND request_id=$2`, sub.UserID, requestID).Scan(&id, &stored)
		if err == nil {
			if stored != fingerprint {
				return "", false, common.ErrIdempotencyConflict
			}
			return id, true, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
	}
	if err := admitContestRegistrationInTx(ctx, tx, sub.ContestID, sub.UserID); err != nil {
		return "", false, err
	}
	slot, err := reserveCodeAdmissionSlot(ctx, tx, maxPending)
	if err != nil {
		return "", false, err
	}
	if err := checkSubmissionRateLimit(ctx, tx, sub.UserID, cooldown); err != nil {
		return "", false, err
	}
	id, err = s.insertSubmission(ctx, tx, sub, &slot)
	if err != nil {
		return "", false, err
	}
	if requestID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO code_submission_requests(user_id,request_id,request_hash,submission_id) VALUES($1,$2,$3,$4)`, sub.UserID, requestID, fingerprint, id); err != nil {
			return "", false, err
		}
	}
	return id, false, tx.Commit()
}

// The partial unique index releases a slot when its parent completes or is deleted.
func reserveCodeAdmissionSlot(ctx context.Context, tx *sql.Tx, maxPending int) (int, error) {
	next := 1
	for next <= maxPending {
		var slot int
		// Filter before taking locks so occupied slots remain independent of this transaction.
		err := tx.QueryRowContext(ctx, `WITH available AS MATERIALIZED (
   SELECT candidate FROM generate_series($1::bigint,$2::bigint) AS candidate
   WHERE NOT EXISTS(SELECT 1 FROM submissions WHERE type='code' AND status='pending' AND (admission_slot IS NULL OR admission_slot>$2))
   AND NOT EXISTS(SELECT 1 FROM submissions WHERE type='code' AND status='pending' AND admission_slot=candidate)
   ) SELECT candidate FROM available
   WHERE pg_try_advisory_xact_lock(hashtextextended('code:slot:' || candidate::text,0))
   LIMIT 1`, next, maxPending).Scan(&slot)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, common.ErrRateLimitExceeded
		}
		if err != nil {
			return 0, err
		}
		// A former holder may have committed after the candidate query's snapshot.
		var occupied bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM submissions WHERE type='code' AND status='pending' AND admission_slot=$1)`, slot).Scan(&occupied); err != nil {
			return 0, err
		}
		if !occupied {
			return slot, nil
		}
		next = slot + 1
	}
	return 0, common.ErrRateLimitExceeded
}
