package stores

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type SubmissionStore struct {
	db *sql.DB
}

func NewSubmissionStore(db *sql.DB) *SubmissionStore {
	return &SubmissionStore{
		db: db,
	}
}

func (s *SubmissionStore) GetSubmissionStatusByID(ctx context.Context, id string) (*models.Submission, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("submission store: db is not initialized")
	}

	const q = `
		SELECT status, user_id
		FROM submissions
		WHERE id = $1
	`
	var sub models.Submission
	sub.ID = id

	row := s.db.QueryRowContext(ctx, q, id)
	if err := row.Scan(&sub.Status, &sub.UserID); err != nil {
		if err == sql.ErrNoRows {
			return nil, common.ErrNotFound
		}
		log.Printf("submission-store: row scan failed for ID %s: %v", id, err)
		return nil, fmt.Errorf("scan submission: %w", err)
	}

	return &sub, nil
}

func (s *SubmissionStore) GetSubmissionDetailsByID(ctx context.Context, id string) (*dto.GetSubmissionDetailsResponse, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("submission store: db is not initialized")
	}

	const q = `
		SELECT user_id, contest_id, problem_id, type, language, choices, status, created_at, runtime, memory
		FROM submissions
		WHERE id = $1
	`
	var sub dto.GetSubmissionDetailsResponse
	sub.ID = id

	var rawChoices sql.NullString

	row := s.db.QueryRowContext(ctx, q, id)
	if err := row.Scan(
		&sub.UserID,
		&sub.ContestID,
		&sub.ProblemID,
		&sub.Type,
		&sub.Language,
		&rawChoices,
		&sub.Status,
		&sub.CreatedAt,
		&sub.Runtime,
		&sub.Memory,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, common.ErrNotFound
		}
		log.Printf("submission-store: row scan failed for ID %s: %v", id, err)
		return nil, fmt.Errorf("scan submission: %w", err)
	}

	sub.Option = []int{}
	if rawChoices.Valid && rawChoices.String != "" && rawChoices.String != "{}" {
		choiceStr := strings.TrimSpace(strings.Trim(rawChoices.String, "{}"))

		if choiceStr != "" {
			parts := strings.Split(choiceStr, ",")

			for _, part := range parts {
				val, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil {
					log.Printf("submission-store: failed to parse choice value '%s': %v", part, err)
					continue
				}
				sub.Option = append(sub.Option, val)
			}
		}
	}

	testCaseResults, err := s.GetTestCaseResultsBySubmissionID(ctx, id)
	if err != nil {
		log.Printf("submission-store: failed to get test case results for submission ID %s: %v", id, err)
		sub.TestCaseResults = []models.TestCaseResult{}
	} else {
		sub.TestCaseResults = testCaseResults
	}

	return &sub, nil
}

func (s *SubmissionStore) GetTestCaseResultsBySubmissionID(ctx context.Context, submissionID string) ([]models.TestCaseResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("submission store: db is not initialized")
	}

	const q = `
		SELECT id, submission_id, test_case_id, status, runtime, memory, created_at
		FROM test_case_results
		WHERE submission_id = $1
		ORDER BY created_at ASC
	`
	rows, err := s.db.QueryContext(ctx, q, submissionID)
	if err != nil {
		return nil, fmt.Errorf("query test case results: %w", err)
	}
	defer rows.Close()

	var results []models.TestCaseResult
	for rows.Next() {
		var res models.TestCaseResult
		if err := rows.Scan(
			&res.ID,
			&res.SubmissionID,
			&res.TestCaseID,
			&res.Status,
			&res.Runtime,
			&res.Memory,
			&res.CreatedAt,
		); err != nil {
			log.Printf("submission-store: failed to scan test case result row for submission  %s: %v", submissionID, err)
			continue
		}
		results = append(results, res)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return results, nil
}

func (s *SubmissionStore) ListUserSubmissionsByProblemID(ctx context.Context, userID, problemID string, page int) ([]models.Submission, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("submission store: db is not initialized")
	}

	const pageSize = 20
	page = max(0, page)
	offset := page * pageSize

	const q = `
		SELECT id, contest_id, problem_id, type, language, status, created_at, runtime, memory
		FROM submissions
		WHERE user_id = $1 AND problem_id = $2
		ORDER BY created_at DESC
		LIMIT $3 OFFSET $4
	`

	rows, err := s.db.QueryContext(ctx, q, userID, problemID, pageSize, offset)
	if err != nil {
		log.Printf("submission-store: query failed: %v", err)
		return nil, fmt.Errorf("query user submissions: %w", err)
	}
	defer rows.Close()

	submissions := make([]models.Submission, 0)
	for rows.Next() {
		var sub models.Submission

		if err := rows.Scan(
			&sub.ID,
			&sub.ContestID,
			&sub.ProblemID,
			&sub.Type,
			&sub.Language,
			&sub.Status,
			&sub.CreatedAt,
			&sub.Runtime,
			&sub.Memory,
		); err != nil {
			log.Printf("submission-store: failed to scan submission row: %v", err)
			continue
		}
		submissions = append(submissions, sub)
	}

	if err := rows.Err(); err != nil {
		log.Printf("submission-store: rows error: %v", err)
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return submissions, nil
}

func (s *SubmissionStore) CreateSubmission(ctx context.Context, sub *models.Submission) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("submission store: db is not initialized")
	}

	sub.ID = uuid.NewString()
	sub.CreatedAt = time.Now().Unix()

	dbType := strings.ToLower(string(sub.Type))
	dbStatus := "pending"

	choiceStrings := make([]string, len(sub.Option))
	for i, choice := range sub.Option {
		choiceStrings[i] = strconv.Itoa(choice)
	}
	mcqChoices := fmt.Sprintf("{%s}", strings.Join(choiceStrings, ","))

	const q = `
		INSERT INTO 
		submissions (id, user_id, contest_id, problem_id, type, language, choices, status, created_at, runtime, memory)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id
	`

	var submissionID string
	err := s.db.QueryRowContext(ctx, q,
		sub.ID,
		sub.UserID,
		sub.ContestID,
		sub.ProblemID,
		dbType,
		sub.Language,
		mcqChoices,
		dbStatus,
		sub.CreatedAt,
		sub.Runtime,
		sub.Memory,
	).Scan(&submissionID)

	if err != nil {
		log.Printf("submission-store: failed to insert submission: %v", err)
		return "", fmt.Errorf("insert submission: %w", err)
	}

	return submissionID, nil
}

// JudgeMCQ compares the stored choices with the problem answer and records the
// result and any score change in one transaction. The first accepted
// submission for a problem earns its score; later accepted submissions remain
// accepted but do not add to the ranking again.
func (s *SubmissionStore) JudgeMCQ(ctx context.Context, submissionID string) (err error) {
	if s == nil || s.db == nil {
		return fmt.Errorf("submission store: db is not initialized")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin MCQ judging transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	const submissionQ = `
		SELECT sub.user_id, sub.contest_id, sub.problem_id, sub.choices,
		       sub.type, sub.status, p.answer, p.type, p.score
		FROM submissions sub
		INNER JOIN problems p
			ON p.id = sub.problem_id AND p.contest_id = sub.contest_id
		WHERE sub.id = $1
		FOR UPDATE OF sub
	`

	var (
		userID, contestID, problemID string
		submissionType, problemType  models.SubmissionType
		status                       models.SubmissionStatus
		choices, answer              pq.Int64Array
		problemScore                 int
	)
	if err = tx.QueryRowContext(ctx, submissionQ, submissionID).Scan(
		&userID,
		&contestID,
		&problemID,
		&choices,
		&submissionType,
		&status,
		&answer,
		&problemType,
		&problemScore,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return common.ErrNotFound
		}
		return fmt.Errorf("load MCQ submission: %w", err)
	}

	if submissionType != models.MCQ || problemType != models.MCQ {
		return fmt.Errorf("submission %s is not for an MCQ problem", submissionID)
	}
	if status != models.Pending {
		return tx.Commit()
	}

	result := models.WrongAnswer
	if mcqAnswersMatch(choices, answer) {
		result = models.Accepted
	}

	// The ranking row is also the per-contest/user lock. Taking this lock before
	// checking earlier accepts makes concurrent correct attempts idempotent.
	const ensureRankingQ = `
		INSERT INTO rankings (contest_id, user_id)
		VALUES ($1, $2)
		ON CONFLICT (contest_id, user_id) DO NOTHING
	`
	if _, err = tx.ExecContext(ctx, ensureRankingQ, contestID, userID); err != nil {
		return fmt.Errorf("ensure ranking: %w", err)
	}
	var currentScore int
	if err = tx.QueryRowContext(ctx, `
		SELECT score
		FROM rankings
		WHERE contest_id = $1 AND user_id = $2
		FOR UPDATE
	`, contestID, userID).Scan(&currentScore); err != nil {
		return fmt.Errorf("lock ranking: %w", err)
	}

	awardScore := false
	if result == models.Accepted {
		const alreadySolvedQ = `
			SELECT EXISTS (
				SELECT 1
				FROM submissions
				WHERE user_id = $1
				  AND contest_id = $2
				  AND problem_id = $3
				  AND status = 'accepted'
				  AND id <> $4
			)
		`
		var alreadySolved bool
		if err = tx.QueryRowContext(ctx, alreadySolvedQ, userID, contestID, problemID, submissionID).Scan(&alreadySolved); err != nil {
			return fmt.Errorf("check prior accepted MCQ submission: %w", err)
		}
		awardScore = !alreadySolved
	}

	if _, err = tx.ExecContext(ctx, `
		UPDATE submissions
		SET status = $2
		WHERE id = $1 AND status = 'pending'
	`, submissionID, result); err != nil {
		return fmt.Errorf("update MCQ submission status: %w", err)
	}

	if awardScore {
		if _, err = tx.ExecContext(ctx, `
			UPDATE rankings
			SET score = score + $3
			WHERE contest_id = $1 AND user_id = $2
		`, contestID, userID, problemScore); err != nil {
			return fmt.Errorf("award MCQ score: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit MCQ judging transaction: %w", err)
	}
	return nil
}

func mcqAnswersMatch(choices, answer []int64) bool {
	if len(answer) == 0 || len(choices) != len(answer) {
		return false
	}

	sortedChoices := slices.Clone(choices)
	sortedAnswer := slices.Clone(answer)
	slices.Sort(sortedChoices)
	slices.Sort(sortedAnswer)
	return slices.Equal(sortedChoices, sortedAnswer)
}

func (s *SubmissionStore) MarkFailed(ctx context.Context, submissionID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("submission store: db is not initialized")
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE submissions SET status = 'judge_error'
		WHERE id = $1 AND status = 'pending'
	`, submissionID)
	if err != nil {
		return fmt.Errorf("mark submission failed: %w", err)
	}
	return nil
}
