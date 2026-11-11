package stores

import (
	"app/internal/common"
	"app/internal/models"
	"context"
	"database/sql"
	"errors"
	"fmt"
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
func (s *SubmissionStore) CreateCodeSubmission(ctx context.Context, sub *models.Submission, requestID, fingerprint string, cooldown int64, maxPending int) (string, bool, error) {
	if s == nil || s.db == nil || maxPending < 1 || cooldown < 1 {
		return "", false, fmt.Errorf("invalid submission admission configuration")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	// The fleet lock covers counting and insertion across every API process.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('code:admission'))`); err != nil {
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
	var pending int
	var userPending bool
	if err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(bool_or(user_id=$1),false) FROM submissions WHERE type='code' AND status='pending'`, sub.UserID).Scan(&pending, &userPending); err != nil {
		return "", false, err
	}
	if pending >= maxPending || userPending {
		return "", false, common.ErrRateLimitExceeded
	}
	if err := checkSubmissionRateLimit(ctx, tx, sub.UserID, cooldown); err != nil {
		return "", false, err
	}
	id, err := s.insertSubmission(ctx, tx, sub)
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
