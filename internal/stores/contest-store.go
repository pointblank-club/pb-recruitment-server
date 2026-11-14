package stores

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/gommon/log"
)

type ContestStore struct {
	db *sql.DB
}

func NewContestStore(db *sql.DB) *ContestStore {
	return &ContestStore{
		db: db,
	}
}

func (s *ContestStore) ListContests(ctx context.Context, page int) ([]models.Contest, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("contest store: db is not initialized")
	}

	const pageSize = 20
	page = max(0, page)
	offset := page * pageSize

	const q = `
		SELECT id, name, registration_start_time, registration_end_time, start_time, end_time, eligible_to, description
		FROM contests
		ORDER BY start_time DESC
		LIMIT $1 OFFSET $2
	`

	rows, err := s.db.QueryContext(ctx, q, pageSize, offset)
	if err != nil {
		log.Printf("contest-store: query failed: %v", err)
		return nil, fmt.Errorf("query contests: %w", err)
	}
	defer rows.Close()

	contests := make([]models.Contest, 0)
	for rows.Next() {
		var c models.Contest
		var eligibility sql.NullString

		if err := rows.Scan(&c.ID, &c.Name, &c.RegistrationStartTime, &c.RegistrationEndTime, &c.StartTime, &c.EndTime, &eligibility, &c.Description); err != nil {
			log.Printf("contest-store: row scan failed: %v", err)
			return nil, fmt.Errorf("scan contest row: %w", err)
		}

		if eligibility.Valid {
			eligibilityYears := strings.Split(eligibility.String, ",")
			for _, yearStr := range eligibilityYears {
				year, err := strconv.Atoi(yearStr)
				if err != nil {
					log.Printf("contest-store: invalid eligibility year: %v", err)
					continue
				}
				c.EligibleTo = append(c.EligibleTo, year)
			}
		}
		contests = append(contests, c)
	}

	if err := rows.Err(); err != nil {
		log.Printf("contest-store: rows error: %v", err)
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return contests, nil
}

func (s *ContestStore) IsRegistered(ctx context.Context, contestID string, userID string) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1
			FROM contest_registrations
			WHERE contest_id = $1 AND user_id = $2
		)
	`

	var exists bool
	err := s.db.QueryRowContext(ctx, q, contestID, userID).Scan(&exists)
	if err != nil {
		log.Printf("contest-store: query failed: %v", err)
		return false, fmt.Errorf("query contest registration: %w", err)
	}

	return exists, nil
}

func (s *ContestStore) CreateContest(ctx context.Context, c *models.Contest) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("contest store: db is not initialized")
	}

	const q = `
        INSERT INTO contests (id, name, registration_start_time, registration_end_time, start_time, end_time, eligible_to, description)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
    `

	eligibilityStr := strings.Join(intSliceToStringSlice(c.EligibleTo), ",")
	_, err := s.db.ExecContext(ctx, q,
		c.ID,
		c.Name,
		c.RegistrationStartTime,
		c.RegistrationEndTime,
		c.StartTime,
		c.EndTime,
		eligibilityStr,
		c.Description,
	)

	if err != nil {
		log.Printf("contest-store: insert failed: %v", err)
		return fmt.Errorf("insert contest: %w", err)
	}

	return nil
}

func intSliceToStringSlice(i []int) []string {
	s := make([]string, len(i))
	for idx, val := range i {
		s[idx] = strconv.Itoa(val)
	}
	return s
}

func (s *ContestStore) UpdateContest(ctx context.Context, c *models.Contest) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("contest store: db is not initialized")
	}

	const q = `
        UPDATE contests
        SET name = $2,
            registration_start_time = $3,
            registration_end_time = $4,
            start_time = $5,
            end_time = $6,
			eligible_to = $7,
			description = $8
        WHERE id = $1
    `
	eligibilityStr := strings.Join(intSliceToStringSlice(c.EligibleTo), ",")
	_, err := s.db.ExecContext(ctx, q,
		c.ID,
		c.Name,
		c.RegistrationStartTime,
		c.RegistrationEndTime,
		c.StartTime,
		c.EndTime,
		eligibilityStr,
		c.Description,
	)

	if err != nil {
		log.Printf("contest-store: update failed: %v", err)
		return fmt.Errorf("update contest: %w", err)
	}
	return nil
}

func (s *ContestStore) DeleteContest(ctx context.Context, contestID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("contest store: db is not initialized")
	}

	const q = `DELETE FROM contests WHERE id = $1`

	_, err := s.db.ExecContext(ctx, q, contestID)
	if err != nil {
		log.Printf("contest-store: delete failed: %v", err)
		return fmt.Errorf("delete contest: %w", err)
	}
	return nil
}

func (s *ContestStore) GetContest(ctx context.Context, contestID string) (*dto.GetContestResponse, error) {
	const q = `
		SELECT id, name, registration_start_time, registration_end_time, start_time, end_time, eligible_to, description
		FROM contests
		WHERE id = $1
	`

	var c dto.GetContestResponse
	var eligibility sql.NullString
	err := s.db.QueryRowContext(ctx, q, contestID).Scan(
		&c.ID, &c.Name, &c.RegistrationStartTime, &c.RegistrationEndTime, &c.StartTime, &c.EndTime, &eligibility, &c.Description,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, common.ContestNotFoundError
		}
		log.Printf("contest-store: query failed: %v", err)
		return nil, fmt.Errorf("query contest: %w", err)
	}

	if eligibility.Valid {
		eligibilityYears := strings.Split(eligibility.String, ",")
		for _, yearStr := range eligibilityYears {
			year, err := strconv.Atoi(yearStr)
			if err != nil {
				log.Printf("contest-store: invalid eligibility year: %v", err)
				continue
			}
			c.EligibleTo = append(c.EligibleTo, year)
		}
	}

	return &c, nil
}

func (s *ContestStore) RegisterUser(ctx context.Context, contestID string, userID string) error {
	const q = `
		INSERT INTO contest_registrations (contest_id, user_id, registered_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (contest_id, user_id) DO NOTHING
		`

	res, err := s.db.ExecContext(ctx, q, contestID, userID, time.Now().Unix())
	if err != nil {
		log.Errorf("contest-store: query failed: %v", err)
		return fmt.Errorf("query contest registration: %w", err)
	}

	// If rows affected is 0, then the user already registered
	affected, err := res.RowsAffected()
	if err != nil {
		log.Errorf("user-store: rows error %v", err)
		return fmt.Errorf("rows error: %w", err)
	}

	if affected == 0 {
		log.Errorf("user-store: user %s already registered", userID)
		return common.UserAlreadyExistsError
	}

	return nil
}

func (s *ContestStore) UnregisterUser(ctx context.Context, contestID string, userID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("contest store: db is not initialized")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin unregister tx: %w", err)
	}
	defer tx.Rollback()

	const lockQ = `
		SELECT c.start_time
		FROM contest_registrations cr
		INNER JOIN contests c ON c.id = cr.contest_id
		WHERE cr.contest_id = $1 AND cr.user_id = $2
		FOR UPDATE OF cr
	`
	var startTime int64
	if err := tx.QueryRowContext(ctx, lockQ, contestID, userID).Scan(&startTime); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			exists, existsErr := s.contestExists(ctx, tx, contestID)
			if existsErr != nil {
				return fmt.Errorf("check contest existence: %w", existsErr)
			}
			if !exists {
				return common.ContestNotFoundError
			}
			log.Errorf("user-store: user %s not registered", userID)
			return common.UserNotFoundError
		}
		log.Errorf("contest-store: lock registration failed: %v", err)
		return fmt.Errorf("lock contest registration: %w", err)
	}

	// Sample time after acquiring the row lock to avoid stale pre-lock eligibility checks.
	now := time.Now().UnixMilli()
	if startTime <= now {
		return common.ErrCannotUnregisterAfterStart
	}

	const delQ = `
		DELETE FROM contest_registrations
		WHERE contest_id = $1 AND user_id = $2
	`
	res, err := tx.ExecContext(ctx, delQ, contestID, userID)
	if err != nil {
		log.Errorf("contest-store: query failed: %v", err)
		return fmt.Errorf("delete contest registration: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		log.Errorf("user-store: rows error %v", err)
		return fmt.Errorf("rows error: %w", err)
	}
	if affected == 0 {
		log.Errorf("user-store: user %s not registered", userID)
		return common.UserNotFoundError
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit unregister tx: %w", err)
	}

	return nil
}

func (s *ContestStore) GetContestRegistrations(ctx context.Context, contestID string) ([]dto.ContestRegistration, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("contest store: db is not initialized")
	}

	const q = `
		SELECT 
			u.id,
			u.name,
			u.email,
			u.usn,
			u.department,
			u.current_year,
			cr.registered_at,
			COALESCE(r.score, 0),
			COALESCE(r.hidden, FALSE),
			COALESCE(r.disqualified, FALSE),
			COALESCE(cr.tab_switch_count, 0),
			COALESCE(cr.is_tab_switch_terminated, FALSE),
			COALESCE(cr.reset_generation, 0),
			cr.ended_at
		FROM contest_registrations cr
		INNER JOIN users u ON cr.user_id = u.id
		LEFT JOIN rankings r ON r.contest_id = cr.contest_id AND r.user_id = cr.user_id
		WHERE cr.contest_id = $1
		ORDER BY cr.registered_at ASC
	`

	rows, err := s.db.QueryContext(ctx, q, contestID)
	if err != nil {
		log.Printf("contest-store: query registrations failed: %v", err)
		return nil, fmt.Errorf("query contest registrations: %w", err)
	}
	defer rows.Close()

	registrations := make([]dto.ContestRegistration, 0)
	for rows.Next() {
		var reg dto.ContestRegistration
		if err := rows.Scan(
			&reg.UserID,
			&reg.Name,
			&reg.Email,
			&reg.USN,
			&reg.Department,
			&reg.CurrentYear,
			&reg.RegisteredAt,
			&reg.Score,
			&reg.Hidden,
			&reg.Disqualified,
			&reg.TabSwitchCount,
			&reg.IsTabSwitchTerminated,
			&reg.ResetGeneration,
			&reg.EndedAt,
		); err != nil {
			log.Printf("contest-store: failed to scan registration row: %v", err)
			continue
		}
		registrations = append(registrations, reg)
	}

	if err := rows.Err(); err != nil {
		log.Printf("contest-store: rows error: %v", err)
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return registrations, nil
}

func boundedTabSwitchHistoryExpr(entryParamIdx, maxEntriesParamIdx int) string {
	return fmt.Sprintf(`(
		WITH combined AS (
			SELECT value, ord
			FROM jsonb_array_elements(COALESCE(contest_registrations.tab_switch_history, '[]'::jsonb) || $%d::jsonb) WITH ORDINALITY AS t(value, ord)
		),
		total AS (
			SELECT count(*) AS n FROM combined
		)
		SELECT COALESCE(jsonb_agg(c.value ORDER BY c.ord), '[]'::jsonb)
		FROM combined c, total
		WHERE c.ord > GREATEST(0, total.n - $%d)
	)`, entryParamIdx, maxEntriesParamIdx)
}

func (s *ContestStore) contestExists(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, contestID string) (bool, error) {
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM contests WHERE id = $1)`, contestID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (s *ContestStore) RecordTabSwitch(
	ctx context.Context,
	contestID string,
	userID string,
	observationID string,
	generation int,
	count int,
) (*dto.TabSwitchResponse, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("contest store: db is not initialized")
	}
	if count != 1 {
		return nil, common.ErrInvalidTabSwitchRequest
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tab switch tx: %w", err)
	}
	defer tx.Rollback()

	const lockQ = `
		SELECT
			c.start_time,
			c.end_time,
			COALESCE(cr.tab_switch_count, 0),
			COALESCE(cr.is_tab_switch_terminated, FALSE),
			COALESCE(cr.reset_generation, 0)
		FROM contest_registrations cr
		INNER JOIN contests c ON c.id = cr.contest_id
		WHERE cr.contest_id = $1 AND cr.user_id = $2
		FOR UPDATE OF cr
	`
	var startTime, endTime int64
	var currentCount, currentGen int
	var currentTerminated bool
	err = tx.QueryRowContext(ctx, lockQ, contestID, userID).Scan(&startTime, &endTime, &currentCount, &currentTerminated, &currentGen)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			exists, existsErr := s.contestExists(ctx, tx, contestID)
			if existsErr != nil {
				return nil, fmt.Errorf("check contest existence: %w", existsErr)
			}
			if !exists {
				return nil, common.ContestNotFoundError
			}
			return nil, common.UserNotRegisteredError
		}
		log.Printf("contest-store: lock registration for tab switch failed: %v", err)
		return nil, fmt.Errorf("lock registration for tab switch: %w", err)
	}

	now := time.Now().UnixMilli()
	if now < startTime || now > endTime {
		return nil, common.ContestNotRunningError
	}

	if generation != currentGen {
		return nil, &common.TabSwitchStaleGenerationError{
			TabSwitchCount: currentCount,
			IsTerminated:   currentTerminated,
			Generation:     currentGen,
		}
	}

	// Termination is sticky until an authorized admin reset; avoid mutating count or history afterward.
	if currentTerminated {
		return &dto.TabSwitchResponse{
			TabSwitchCount: currentCount,
			IsTerminated:   true,
			Generation:     currentGen,
		}, nil
	}

	const dedupQ = `
		INSERT INTO tab_switch_observations (contest_id, user_id, generation, observation_id, created_at)
		VALUES ($1, $2, $3, $4::uuid, $5)
		ON CONFLICT (contest_id, user_id, generation, observation_id) DO NOTHING
	`
	dedupRes, err := tx.ExecContext(ctx, dedupQ, contestID, userID, currentGen, observationID, now)
	if err != nil {
		log.Printf("contest-store: insert tab switch observation failed: %v", err)
		return nil, fmt.Errorf("insert tab switch observation: %w", err)
	}
	inserted, err := dedupRes.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("observation rows affected: %w", err)
	}
	if inserted == 0 {
		// Retry/duplicate observation returns the previously persisted state.
		return &dto.TabSwitchResponse{
			TabSwitchCount: currentCount,
			IsTerminated:   currentTerminated,
			Generation:     currentGen,
		}, nil
	}

	newCount := currentCount + count
	if newCount > dto.TabSwitchTerminationThreshold {
		newCount = dto.TabSwitchTerminationThreshold
	}
	newTerminated := newCount >= dto.TabSwitchTerminationThreshold

	entryBytes, err := json.Marshal([]map[string]any{{
		"timestamp":      now,
		"count":          newCount,
		"delta":          count,
		"observation_id": observationID,
		"generation":     currentGen,
	}})
	if err != nil {
		return nil, fmt.Errorf("marshal tab switch history: %w", err)
	}

	updateQ := `
		UPDATE contest_registrations
		SET
			tab_switch_count = $3,
			is_tab_switch_terminated = $4,
			tab_switch_history = ` + boundedTabSwitchHistoryExpr(5, 6) + `
		WHERE contest_id = $1 AND user_id = $2
	`
	if _, err := tx.ExecContext(ctx, updateQ, contestID, userID, newCount, newTerminated, string(entryBytes), dto.TabSwitchMaxHistoryEntries); err != nil {
		log.Printf("contest-store: update tab switch state failed: %v", err)
		return nil, fmt.Errorf("update tab switch state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tab switch tx: %w", err)
	}

	return &dto.TabSwitchResponse{
		TabSwitchCount: newCount,
		IsTerminated:   newTerminated,
		Generation:     currentGen,
	}, nil
}

func (s *ContestStore) GetCandidateSessionStatus(ctx context.Context, contestID string, userID string) (*dto.CandidateSessionStatusResponse, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("contest store: db is not initialized")
	}

	const q = `
		SELECT
			(cr.user_id IS NOT NULL) AS is_registered,
			COALESCE(cr.tab_switch_count, 0),
			COALESCE(cr.is_tab_switch_terminated, FALSE),
			COALESCE(cr.reset_generation, 0),
			COALESCE(cr.ended_at IS NOT NULL, FALSE),
			cr.ended_at
		FROM contests c
		LEFT JOIN contest_registrations cr
			ON cr.contest_id = c.id AND cr.user_id = $2
		WHERE c.id = $1
	`

	var isRegistered bool
	var res dto.CandidateSessionStatusResponse
	err := s.db.QueryRowContext(ctx, q, contestID, userID).Scan(
		&isRegistered,
		&res.TabSwitchCount,
		&res.IsTabSwitchTerminated,
		&res.Generation,
		&res.IsSessionEnded,
		&res.EndedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, common.ContestNotFoundError
		}
		log.Printf("contest-store: get session status failed: %v", err)
		return nil, fmt.Errorf("get candidate session status: %w", err)
	}
	if !isRegistered {
		return nil, common.UserNotRegisteredError
	}

	return &res, nil
}

func (s *ContestStore) ResetContestantTabSwitches(ctx context.Context, contestID string, userID string, adminID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("contest store: db is not initialized")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin reset tab switches tx: %w", err)
	}
	defer tx.Rollback()

	const lockQ = `
		SELECT COALESCE(reset_generation, 0)
		FROM contest_registrations
		WHERE contest_id = $1 AND user_id = $2
		FOR UPDATE
	`
	var currentGen int
	if err := tx.QueryRowContext(ctx, lockQ, contestID, userID).Scan(&currentGen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			exists, existsErr := s.contestExists(ctx, tx, contestID)
			if existsErr != nil {
				return 0, fmt.Errorf("check contest existence: %w", existsErr)
			}
			if !exists {
				return 0, common.ContestNotFoundError
			}
			return 0, common.UserNotRegisteredError
		}
		log.Printf("contest-store: lock registration for reset failed: %v", err)
		return 0, fmt.Errorf("lock registration for reset: %w", err)
	}

	newGen := currentGen + 1
	now := time.Now().UnixMilli()
	auditBytes, err := json.Marshal([]map[string]any{{
		"action":     "admin_reset",
		"admin_id":   adminID,
		"generation": newGen,
		"timestamp":  now,
	}})
	if err != nil {
		return 0, fmt.Errorf("marshal admin reset audit: %w", err)
	}

	updateQ := `
		UPDATE contest_registrations
		SET
			tab_switch_count = 0,
			is_tab_switch_terminated = FALSE,
			reset_generation = $3,
			tab_switch_history = ` + boundedTabSwitchHistoryExpr(4, 5) + `
		WHERE contest_id = $1 AND user_id = $2
	`
	if _, err := tx.ExecContext(ctx, updateQ, contestID, userID, newGen, string(auditBytes), dto.TabSwitchMaxHistoryEntries); err != nil {
		log.Printf("contest-store: reset tab switches failed: %v", err)
		return 0, fmt.Errorf("reset contestant tab switches: %w", err)
	}

	const cleanupQ = `
		DELETE FROM tab_switch_observations
		WHERE contest_id = $1 AND user_id = $2 AND generation < $3
	`
	if _, err := tx.ExecContext(ctx, cleanupQ, contestID, userID, newGen); err != nil {
		log.Printf("contest-store: cleanup prior tab switch observations failed: %v", err)
		return 0, fmt.Errorf("cleanup prior tab switch observations: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit reset tab switches tx: %w", err)
	}

	return newGen, nil
}

func (s *ContestStore) EndContestSession(ctx context.Context, contestID string, userID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("contest store: db is not initialized")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin end contest session tx: %w", err)
	}
	defer tx.Rollback()

	// 1. Lock candidate registration row to serialize against concurrent submissions/resets
	const lockRegQ = `
		SELECT ended_at
		FROM contest_registrations
		WHERE contest_id = $1 AND user_id = $2
		FOR UPDATE
	`
	var existingEndedAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, lockRegQ, contestID, userID).Scan(&existingEndedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			exists, existsErr := s.contestExists(ctx, tx, contestID)
			if existsErr != nil {
				return fmt.Errorf("check contest existence: %w", existsErr)
			}
			if !exists {
				return common.ContestNotFoundError
			}
			return common.UserNotRegisteredError
		}
		return fmt.Errorf("lock registration for end session: %w", err)
	}

	// 2. Sample ended_at timestamp AFTER acquiring the registration write lock
	now := time.Now().UnixMilli()

	// 3. Validate contest timing inside the store transaction
	const contestTimingQ = `
		SELECT start_time, end_time
		FROM contests
		WHERE id = $1
	`
	var startTime, endTime int64
	if err := tx.QueryRowContext(ctx, contestTimingQ, contestID).Scan(&startTime, &endTime); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return common.ContestNotFoundError
		}
		return fmt.Errorf("check contest timing: %w", err)
	}

	if now < startTime || now > endTime {
		return common.ContestNotRunningError
	}

	// 4. Idempotent: preserve original timestamp if already ended
	if existingEndedAt.Valid {
		return nil
	}

	// 5. Update ended_at
	const updateQ = `
		UPDATE contest_registrations
		SET ended_at = $3
		WHERE contest_id = $1 AND user_id = $2
	`
	if _, err := tx.ExecContext(ctx, updateQ, contestID, userID, now); err != nil {
		return fmt.Errorf("update contest session ended_at: %w", err)
	}

	return tx.Commit()
}
