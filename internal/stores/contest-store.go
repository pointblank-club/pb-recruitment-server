package stores

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"context"
	"database/sql"
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
	const q = `
		DELETE FROM contest_registrations
		WHERE contest_id = $1 AND user_id = $2
		`

	res, err := s.db.ExecContext(ctx, q, contestID, userID)
	if err != nil {
		log.Errorf("contest-store: query failed: %v", err)
		return fmt.Errorf("query contest registration: %w", err)
	}

	// If rows affected is 0, then the user never registered
	affected, err := res.RowsAffected()
	if err != nil {
		log.Errorf("user-store: rows error %v", err)
		return fmt.Errorf("rows error: %w", err)
	}

	if affected == 0 {
		log.Errorf("user-store: user %s not registered", userID)
		return common.UserNotFoundError
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
			COALESCE(cr.is_tab_switch_terminated, FALSE)
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

func (s *ContestStore) RecordTabSwitch(ctx context.Context, contestID string, userID string, count int, ip string, userAgent string) (*dto.TabSwitchResponse, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("contest store: db is not initialized")
	}

	const q = `
		WITH new_vals AS (
			SELECT 
				CASE WHEN $3 > 0 THEN GREATEST(COALESCE(tab_switch_count, 0) + 1, $3) ELSE COALESCE(tab_switch_count, 0) + 1 END AS new_count,
				COALESCE(is_tab_switch_terminated, FALSE) AS was_terminated
			FROM contest_registrations
			WHERE contest_id = $1 AND user_id = $2
		)
		UPDATE contest_registrations cr
		SET
			tab_switch_count = nv.new_count,
			is_tab_switch_terminated = (nv.was_terminated OR nv.new_count >= 5),
			tab_switch_history = CASE
				WHEN nv.was_terminated THEN cr.tab_switch_history
				ELSE COALESCE(cr.tab_switch_history, '[]'::jsonb) || jsonb_build_array(
					jsonb_build_object(
						'timestamp', $4::bigint,
						'count', nv.new_count,
						'ip', $5::text,
						'user_agent', $6::text
					)
				)
			END
		FROM new_vals nv
		WHERE cr.contest_id = $1 AND cr.user_id = $2
		RETURNING cr.tab_switch_count, cr.is_tab_switch_terminated;
	`

	now := time.Now().UnixMilli()
	var res dto.TabSwitchResponse
	err := s.db.QueryRowContext(ctx, q, contestID, userID, count, now, ip, userAgent).Scan(&res.TabSwitchCount, &res.IsTerminated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, common.UserNotFoundError
		}
		log.Printf("contest-store: record tab switch failed: %v", err)
		return nil, fmt.Errorf("record tab switch: %w", err)
	}

	return &res, nil
}

func (s *ContestStore) GetCandidateSessionStatus(ctx context.Context, contestID string, userID string) (*dto.CandidateSessionStatusResponse, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("contest store: db is not initialized")
	}

	const q = `
		SELECT 
			COALESCE(tab_switch_count, 0),
			COALESCE(is_tab_switch_terminated, FALSE),
			COALESCE(is_submitted, FALSE)
		FROM contest_registrations
		WHERE contest_id = $1 AND user_id = $2
	`

	var res dto.CandidateSessionStatusResponse
	err := s.db.QueryRowContext(ctx, q, contestID, userID).Scan(&res.TabSwitchCount, &res.IsTabSwitchTerminated, &res.IsSubmitted)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, common.UserNotFoundError
		}
		log.Printf("contest-store: get session status failed: %v", err)
		return nil, fmt.Errorf("get candidate session status: %w", err)
	}

	return &res, nil
}

func (s *ContestStore) ResetContestantTabSwitches(ctx context.Context, contestID string, userID string, adminID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("contest store: db is not initialized")
	}

	const q = `
		UPDATE contest_registrations
		SET
			tab_switch_count = 0,
			is_tab_switch_terminated = FALSE,
			is_submitted = FALSE,
			tab_switch_history = COALESCE(tab_switch_history, '[]'::jsonb) || jsonb_build_array(
				jsonb_build_object(
					'action', 'admin_reset',
					'admin_id', $3::text,
					'timestamp', $4::bigint
				)
			)
		WHERE contest_id = $1 AND user_id = $2
	`

	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx, q, contestID, userID, adminID, now)
	if err != nil {
		log.Printf("contest-store: reset tab switches failed: %v", err)
		return fmt.Errorf("reset contestant tab switches: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return common.UserNotFoundError
	}

	return nil
}
