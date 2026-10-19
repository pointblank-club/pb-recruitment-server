package stores

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"context"
	"database/sql"
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
			COALESCE(r.disqualified, FALSE)
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
