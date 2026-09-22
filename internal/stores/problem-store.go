package stores

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/lib/pq"
)

type ProblemStore struct {
	db *sql.DB
}

func NewProblemStore(db *sql.DB) *ProblemStore {
	return &ProblemStore{
		db: db,
	}
}

func (s *ProblemStore) CreateProblem(ctx context.Context, p *models.Problem) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("problem store: db is not initialized")
	}

	const q = `
        INSERT INTO problems (id, contest_id, name, score, type, answer, description, has_multiple_answers)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
    `
	_, err := s.db.ExecContext(ctx, q,
		p.ID,
		p.ContestID,
		p.Name,
		p.Score,
		p.Type,
		pq.Array(p.Answer),
		p.Description,
		p.HasMultipleAnswers,
	)

	if err != nil {
		log.Printf("problem-store: insert failed: %v", err)
		return fmt.Errorf("insert problem: %w", err)
	}

	return nil
}

func (s *ProblemStore) UpdateProblem(ctx context.Context, p *models.Problem) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("problem store: db is not initialized")
	}

	const q = `
        UPDATE problems
        SET name = $3,
            score = $4,
            type = $5,
			answer = $6,
			has_multiple_answers = $7,
			description = $8
        WHERE id = $1 AND contest_id = $2
    `

	_, err := s.db.ExecContext(ctx, q,
		p.ID,
		p.ContestID,
		p.Name,
		p.Score,
		p.Type,
		pq.Array(p.Answer),
		p.HasMultipleAnswers,
		p.Description,
	)

	if err != nil {
		log.Printf("problem-store: update failed: %v", err)
		return fmt.Errorf("update problem: %w", err)
	}

	return nil
}

func (s *ProblemStore) DeleteProblem(ctx context.Context, contestID string, problemID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("problem store: db is not initialized")
	}

	const q = `DELETE FROM problems WHERE id = $1 AND contest_id = $2`

	_, err := s.db.ExecContext(ctx, q, problemID, contestID)

	if err != nil {
		log.Printf("problem-store: delete failed: %v", err)
		return fmt.Errorf("delete problem: %w", err)
	}

	return nil
}

func (s *ProblemStore) GetProblemList(ctx context.Context, contestID string) ([]dto.ProblemOverview, error) {
	const q = `
		SELECT id, name, score, type
		FROM problems
		WHERE contest_id = $1
	`

	rows, err := s.db.QueryContext(ctx, q, contestID)
	if err != nil {
		log.Printf("problem-store: query failed: %v", err)
		return nil, fmt.Errorf("query contest problems: %w", err)
	}
	defer rows.Close()

	var problems []dto.ProblemOverview
	for rows.Next() {
		var p dto.ProblemOverview

		if err := rows.Scan(&p.ID, &p.Name, &p.Score, &p.Type); err != nil {
			log.Printf("problem-store: row scan failed: %v", err)
			return nil, fmt.Errorf("scan problem row: %w", err)
		}

		problems = append(problems, p)
	}

	if err := rows.Err(); err != nil {
		log.Printf("problem-store: rows error: %v", err)
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if len(problems) == 0 {
		log.Printf("Failed to find contest problems for contest %s", contestID)
		return nil, common.ContestNotFoundError
	}

	return problems, nil
}

func (s *ProblemStore) GetProblem(ctx context.Context, problemID string, contestID string) (*dto.GetProblemStatementResponse, error) {
	const q = `
		SELECT id, contest_id, name, description, score, type
		FROM problems
		WHERE id = $1 AND contest_id = $2
	`

	var p dto.GetProblemStatementResponse
	var desc sql.NullString

	err := s.db.QueryRowContext(ctx, q, problemID, contestID).Scan(
		&p.ProblemID, &p.ContestID, &p.Name, &desc, &p.Score, &p.Type,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("Failed to find problem for contest %s and problem %s", contestID, problemID)
			return nil, common.ContestNotFoundError
		}
		log.Printf("problem-store: query failed: %v", err)
		return nil, fmt.Errorf("query problem: %w", err)
	}

	if desc.Valid {
		p.Description = desc.String
	}

	return &p, nil
}
