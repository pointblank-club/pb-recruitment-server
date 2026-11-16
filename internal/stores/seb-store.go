package stores

import (
	"app/internal/common"
	"app/internal/models/dto"
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

type SEBStore struct{ db *sql.DB }

func NewSEBStore(db *sql.DB) *SEBStore { return &SEBStore{db: db} }

func (s *SEBStore) Reset(ctx context.Context, contestID, userID, adminID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var generation, count int
	err = tx.QueryRowContext(ctx, `SELECT browser_session_generation FROM contest_registrations WHERE contest_id=$1 AND user_id=$2 FOR UPDATE`, contestID, userID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, common.UserNotRegisteredError
	}
	if err != nil {
		return 0, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM contest_browser_sessions WHERE contest_id=$1 AND user_id=$2 AND generation=$3`, contestID, userID, generation).Scan(&count); err != nil {
		return 0, err
	}
	generation++
	if _, err := tx.ExecContext(ctx, `UPDATE contest_registrations SET browser_session_generation=$3,is_browser_session_terminated=FALSE WHERE contest_id=$1 AND user_id=$2`, contestID, userID, generation); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO browser_session_resets(contest_id,user_id,generation,admin_id,previous_count) VALUES($1,$2,$3,$4,$5)`, contestID, userID, generation, adminID, count); err != nil {
		return 0, err
	}
	return generation, tx.Commit()
}

// Serialize admission on the registration, so simultaneous requests cannot exceed the allowance.
func (s *SEBStore) Observe(ctx context.Context, contestID, userID, token, attemptID string, detected, create bool, expectedGeneration *int) (*dto.BrowserSessionResponse, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var live bool
	err = tx.QueryRowContext(ctx, `SELECT start_time <= (extract(epoch FROM clock_timestamp())*1000)::bigint
		AND end_time >= (extract(epoch FROM clock_timestamp())*1000)::bigint FROM contests WHERE id=$1 FOR SHARE`, contestID).Scan(&live)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", common.ContestNotFoundError
	}
	if err != nil {
		return nil, "", err
	}
	if !live {
		if create {
			return nil, "", common.ContestNotRunningError
		}
		return &dto.BrowserSessionResponse{}, "", nil
	}
	res := &dto.BrowserSessionResponse{IsSEB: detected}
	var ended, tabTerminated bool
	err = tx.QueryRowContext(ctx, `SELECT seb_not_detected,is_browser_session_terminated,ended_at IS NOT NULL,is_tab_switch_terminated,browser_session_generation
		FROM contest_registrations WHERE contest_id=$1 AND user_id=$2 FOR UPDATE`, contestID, userID).
		Scan(&res.SEBNotDetected, &res.IsTerminated, &ended, &tabTerminated, &res.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", common.UserNotRegisteredError
	}
	if err != nil {
		return nil, "", err
	}
	if expectedGeneration != nil && *expectedGeneration != res.Generation {
		return nil, "", &common.BrowserSessionStaleError{Generation: res.Generation}
	}
	if ended {
		return nil, "", common.ErrContestSessionEnded
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM contest_browser_sessions WHERE contest_id=$1 AND user_id=$2 AND generation=$3`, contestID, userID, res.Generation).Scan(&res.SessionCount); err != nil {
		return nil, "", err
	}
	if !detected {
		res.SEBNotDetected = true
		if _, err := tx.ExecContext(ctx, `UPDATE contest_registrations SET seb_not_detected=TRUE WHERE contest_id=$1 AND user_id=$2`, contestID, userID); err != nil {
			return nil, "", err
		}
		if err := tx.Commit(); err != nil {
			return nil, "", err
		}
		return res, "", nil
	}
	var savedToken string
	if parsed, err := uuid.Parse(token); err == nil {
		err = tx.QueryRowContext(ctx, `SELECT token::text FROM contest_browser_sessions WHERE contest_id=$1 AND user_id=$2 AND token=$3 AND generation=$4`, contestID, userID, parsed.String(), res.Generation).Scan(&savedToken)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, "", err
		}
	}
	if tabTerminated && create && savedToken == "" {
		return nil, "", common.ErrTabSwitchTerminated
	}
	res.CookieConfirmed = savedToken != ""
	if create && !res.IsTerminated && savedToken == "" {
		var confirmed bool
		err = tx.QueryRowContext(ctx, `SELECT token::text,confirmed FROM contest_browser_sessions WHERE contest_id=$1 AND user_id=$2 AND attempt_id=$3 AND generation=$4`, contestID, userID, attemptID, res.Generation).Scan(&savedToken, &confirmed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, "", err
		}
		if confirmed {
			return nil, "", common.ErrBrowserSessionAttemptUsed
		}
		if savedToken == "" {
			if res.SessionCount >= dto.MaxBrowserSessions {
				res.IsTerminated = true
			} else {
				savedToken = uuid.NewString()
				_, err = tx.ExecContext(ctx, `INSERT INTO contest_browser_sessions(contest_id,user_id,token,attempt_id,generation) VALUES($1,$2,$3,$4,$5)`, contestID, userID, savedToken, attemptID, res.Generation)
				if err != nil {
					return nil, "", err
				}
				res.SessionCount++
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE contest_registrations SET is_browser_session_terminated=$3 WHERE contest_id=$1 AND user_id=$2`, contestID, userID, res.IsTerminated); err != nil {
		return nil, "", err
	}
	if res.CookieConfirmed {
		if _, err := tx.ExecContext(ctx, `UPDATE contest_browser_sessions SET confirmed=TRUE WHERE contest_id=$1 AND user_id=$2 AND token=$3`, contestID, userID, savedToken); err != nil {
			return nil, "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	if !create && !res.IsTerminated && savedToken == "" {
		return nil, "", common.ErrBrowserSessionRequired
	}
	return res, savedToken, nil
}
