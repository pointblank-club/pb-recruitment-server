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
		INSERT INTO problems (id, contest_id, name, score, type, answer, options, description, has_multiple_answers, testcases, time_limit, memory_limit)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		  CASE WHEN $5::problem_type='code' THEN COALESCE(NULLIF($11,0),1000) END,
		  CASE WHEN $5::problem_type='code' THEN COALESCE(NULLIF($12,0),256) END)
        RETURNING COALESCE(time_limit,0), COALESCE(memory_limit,0)
    `
	err := s.db.QueryRowContext(ctx, q,
		p.ID,
		p.ContestID,
		p.Name,
		p.Score,
		p.Type,
		pq.Array(p.Answer),
		pq.Array(p.Options),
		p.Description,
		p.HasMultipleAnswers,
		p.Testcases,
		p.TimeLimit, p.MemoryLimit,
	).Scan(&p.TimeLimit, &p.MemoryLimit)

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
			options = $7,
			has_multiple_answers = $8,
			description = $9,
			testcases = COALESCE(NULLIF($10, ''), testcases),
			time_limit = CASE WHEN $5::problem_type='code' THEN COALESCE(NULLIF($11,0),time_limit,1000) END,
			memory_limit = CASE WHEN $5::problem_type='code' THEN COALESCE(NULLIF($12,0),memory_limit,256) END
        WHERE id = $1 AND contest_id = $2
        RETURNING COALESCE(time_limit,0), COALESCE(memory_limit,0)
    `

	err := s.db.QueryRowContext(ctx, q,
		p.ID,
		p.ContestID,
		p.Name,
		p.Score,
		p.Type,
		pq.Array(p.Answer),
		pq.Array(p.Options),
		p.HasMultipleAnswers,
		p.Description,
		p.Testcases,
		p.TimeLimit, p.MemoryLimit,
	).Scan(&p.TimeLimit, &p.MemoryLimit)

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
		SELECT id, name, score, type,
		  CASE WHEN type='code' THEN COALESCE(time_limit,1000) ELSE 0 END,
		  CASE WHEN type='code' THEN COALESCE(memory_limit,256) ELSE 0 END
		FROM problems
		WHERE contest_id = $1
	`

	rows, err := s.db.QueryContext(ctx, q, contestID)
	if err != nil {
		log.Printf("problem-store: query failed: %v", err)
		return nil, fmt.Errorf("query contest problems: %w", err)
	}
	defer rows.Close()

	problems := make([]dto.ProblemOverview, 0)
	for rows.Next() {
		var p dto.ProblemOverview

		if err := rows.Scan(&p.ID, &p.Name, &p.Score, &p.Type, &p.TimeLimit, &p.MemoryLimit); err != nil {
			log.Printf("problem-store: row scan failed: %v", err)
			return nil, fmt.Errorf("scan problem row: %w", err)
		}

		problems = append(problems, p)
	}

	if err := rows.Err(); err != nil {
		log.Printf("problem-store: rows error: %v", err)
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return problems, nil
}

func (s *ProblemStore) GetProblem(ctx context.Context, problemID string, contestID string) (*dto.GetProblemStatementResponse, error) {
	const q = `
		SELECT id, contest_id, name, COALESCE(description,''), score, type,
		       COALESCE(answer, '{}'::integer[]), COALESCE(options, '{}'::text[]),
		       COALESCE(testcases,''),
		       CASE WHEN type='code' THEN COALESCE(time_limit,1000) ELSE 0 END,
		       CASE WHEN type='code' THEN COALESCE(memory_limit,256) ELSE 0 END
		FROM problems
		WHERE id = $1 AND contest_id = $2
	`

	var p dto.GetProblemStatementResponse
	var answers pq.Int64Array
	var options pq.StringArray

	err := s.db.QueryRowContext(ctx, q, problemID, contestID).Scan(
		&p.ProblemID, &p.ContestID, &p.Name, &p.Description, &p.Score, &p.Type,
		&answers, &options, &p.TestcasesKey, &p.TimeLimit, &p.MemoryLimit,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("Failed to find problem for contest %s and problem %s", contestID, problemID)
			return nil, common.ContestNotFoundError
		}
		log.Printf("problem-store: query failed: %v", err)
		return nil, fmt.Errorf("query problem: %w", err)
	}

	p.Answer = make([]int, len(answers))
	for i, answer := range answers {
		p.Answer[i] = int(answer)
	}
	p.Options = []string(options)

	return &p, nil
}
