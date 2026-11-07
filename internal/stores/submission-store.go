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
	"github.com/jackc/pgx/v5/pgconn"
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

func (s *SubmissionStore) GetUserProblemProgress(ctx context.Context, userID, contestID string) (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("submission store: db is not initialized")
	}
	const q = `
		SELECT problem_id,
		       CASE WHEN bool_or(status = 'accepted') THEN 'solved'
		            WHEN (array_agg(status::text ORDER BY created_at DESC, submission_seq DESC))[1] IN ('pending', 'processing') THEN 'judging'
		            ELSE 'failed' END
		FROM submissions
		WHERE user_id = $1 AND contest_id = $2
		GROUP BY problem_id
	`
	rows, err := s.db.QueryContext(ctx, q, userID, contestID)
	if err != nil {
		return nil, fmt.Errorf("query user problem progress: %w", err)
	}
	defer rows.Close()

	progress := make(map[string]string)
	for rows.Next() {
		var problemID, status string
		if err := rows.Scan(&problemID, &status); err != nil {
			return nil, fmt.Errorf("scan user problem progress: %w", err)
		}
		progress[problemID] = status
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read user problem progress: %w", err)
	}
	return progress, nil
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
		ORDER BY CASE WHEN test_case_id ~ '^[0-9]+$' THEN test_case_id::bigint ELSE 9223372036854775807 END ASC, created_at ASC, id ASC
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

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *SubmissionStore) insertSubmission(ctx context.Context, runner queryRower, sub *models.Submission) (string, error) {
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
	err := runner.QueryRowContext(ctx, q,
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
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_submissions_user_mcq" {
			return "", common.ErrSubmissionAlreadyExists
		}
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == "idx_submissions_user_mcq" {
			return "", common.ErrSubmissionAlreadyExists
		}
		log.Printf("submission-store: failed to insert submission: %v", err)
		return "", fmt.Errorf("insert submission: %w", err)
	}

	return submissionID, nil
}

func (s *SubmissionStore) CreateSubmission(ctx context.Context, sub *models.Submission) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("submission store: db is not initialized")
	}
	return s.insertSubmission(ctx, s.db, sub)
}

func (s *SubmissionStore) CreateMCQSubmission(ctx context.Context, sub *models.Submission, cooldownSeconds int64) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("submission store: db is not initialized")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin mcq submission tx: %w", err)
	}
	defer tx.Rollback()

	// ponytail: advisory user lock serializes MCQ admission per user to prevent duplicate/rate-limit races
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('sub:user:' || $1))`, sub.UserID); err != nil {
		return "", fmt.Errorf("acquire user mcq admission lock: %w", err)
	}

	var exists bool
	const checkDupQ = `
		SELECT EXISTS(
			SELECT 1 FROM submissions
			WHERE user_id = $1 AND contest_id = $2 AND problem_id = $3 AND type = 'mcq'
		)
	`
	if err := tx.QueryRowContext(ctx, checkDupQ, sub.UserID, sub.ContestID, sub.ProblemID).Scan(&exists); err != nil {
		return "", fmt.Errorf("check duplicate mcq submission: %w", err)
	}
	if exists {
		return "", common.ErrSubmissionAlreadyExists
	}

	if cooldownSeconds > 0 {
		const rateLimitQ = `
			INSERT INTO submission_rate_limits (user_id, last_submission_at)
			VALUES ($1, floor(extract(epoch from clock_timestamp()))::bigint)
			ON CONFLICT (user_id) DO UPDATE
			SET last_submission_at = floor(extract(epoch from clock_timestamp()))::bigint
			WHERE floor(extract(epoch from clock_timestamp()))::bigint - submission_rate_limits.last_submission_at >= $2
		`
		res, err := tx.ExecContext(ctx, rateLimitQ, sub.UserID, cooldownSeconds)
		if err != nil {
			return "", fmt.Errorf("check submission rate limit: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return "", fmt.Errorf("rate limit rows affected: %w", err)
		}
		if affected == 0 {
			return "", common.ErrRateLimitExceeded
		}
	}

	submissionID, err := s.insertSubmission(ctx, tx, sub)
	if err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit mcq submission: %w", err)
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

func (s *SubmissionStore) PendingCodeWithoutExecutions(ctx context.Context, cutoff int64, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id::text FROM submissions s WHERE s.type='code' AND s.status='pending' AND s.created_at <= $1 AND NOT EXISTS (SELECT 1 FROM submission_executions e WHERE e.submission_id=s.id) ORDER BY s.created_at,s.id LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *SubmissionStore) CheckSubmissionRateLimit(ctx context.Context, userID string, cooldownSeconds int64) error {
	if s == nil || s.db == nil {
		return nil
	}

	if cooldownSeconds <= 0 {
		return nil
	}

	// Conflict updates must sample the clock after acquiring the row lock.
	const q = `
		INSERT INTO submission_rate_limits (user_id, last_submission_at)
		VALUES ($1, floor(extract(epoch from clock_timestamp()))::bigint)
		ON CONFLICT (user_id) DO UPDATE
		SET last_submission_at = floor(extract(epoch from clock_timestamp()))::bigint
		WHERE floor(extract(epoch from clock_timestamp()))::bigint - submission_rate_limits.last_submission_at >= $2
	`

	res, err := s.db.ExecContext(ctx, q, userID, cooldownSeconds)
	if err != nil {
		return fmt.Errorf("check submission rate limit: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rate limit rows affected: %w", err)
	}

	if affected == 0 {
		return common.ErrRateLimitExceeded
	}

	return nil
}

func (s *SubmissionStore) CheckRunCapacity(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("run capacity database unavailable")
	}
	// Reserve before checking backlog so a Run burst checks it at most once per second.
	// shortcut: admitted Runs may finish ahead of new submits; measure judge capacity before raising this limit.
	if err := s.CheckSubmissionRateLimit(ctx, "run:capacity", 1); err != nil {
		return err
	}
	var pending bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM submissions WHERE type='code' AND status='pending')`).Scan(&pending); err != nil {
		return fmt.Errorf("check run capacity: %w", err)
	}
	if pending {
		return common.ErrRateLimitExceeded
	}
	return nil
}

func (s *SubmissionStore) ListAdminSubmissions(ctx context.Context, contestID, userID, problemID string, page, pageSize int) ([]dto.AdminSubmissionItem, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("submission store: db is not initialized")
	}

	if pageSize <= 0 {
		pageSize = 50
	} else if pageSize > 200 {
		pageSize = 200
	}
	page = max(0, page)
	offset := page * pageSize

	const countQ = `
		SELECT COUNT(*)
		FROM submissions s
		WHERE s.contest_id = $1 AND s.type = 'code'
		  AND ($2 = '' OR s.user_id = $2)
		  AND ($3 = '' OR s.problem_id = $3)
	`
	var totalCount int
	if err := s.db.QueryRowContext(ctx, countQ, contestID, userID, problemID).Scan(&totalCount); err != nil {
		return nil, 0, fmt.Errorf("count admin submissions: %w", err)
	}

	const q = `
		SELECT 
			s.id,
			s.user_id,
			COALESCE(u.name, 'Unknown'),
			COALESCE(u.email, ''),
			s.contest_id,
			s.problem_id,
			COALESCE(p.name, 'Unknown Problem'),
			s.type,
			COALESCE(s.language, ''),
			s.status,
			s.created_at,
			COALESCE(s.runtime, 0),
			COALESCE(s.memory, 0)
		FROM submissions s
		LEFT JOIN users u ON s.user_id = u.id
		LEFT JOIN problems p ON s.problem_id = p.id
		WHERE s.contest_id = $1 AND s.type = 'code'
		  AND ($2 = '' OR s.user_id = $2)
		  AND ($3 = '' OR s.problem_id = $3)
		ORDER BY s.created_at DESC
		LIMIT $4 OFFSET $5
	`

	rows, err := s.db.QueryContext(ctx, q, contestID, userID, problemID, pageSize, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query admin submissions: %w", err)
	}
	defer rows.Close()

	items := make([]dto.AdminSubmissionItem, 0)
	for rows.Next() {
		var item dto.AdminSubmissionItem
		if err := rows.Scan(
			&item.ID,
			&item.UserID,
			&item.UserName,
			&item.UserEmail,
			&item.ContestID,
			&item.ProblemID,
			&item.ProblemName,
			&item.Type,
			&item.Language,
			&item.Status,
			&item.CreatedAt,
			&item.Runtime,
			&item.Memory,
		); err != nil {
			log.Printf("submission-store: failed to scan admin submission row: %v", err)
			continue
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("row iteration error: %w", err)
	}

	return items, totalCount, nil
}

func (s *SubmissionStore) GetAdminSubmissionDetails(ctx context.Context, submissionID string) (*dto.AdminSubmissionDetails, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("submission store: db is not initialized")
	}

	const q = `
		SELECT 
			s.id,
			s.user_id,
			COALESCE(u.name, 'Unknown'),
			COALESCE(u.email, ''),
			s.contest_id,
			s.problem_id,
			COALESCE(p.name, 'Unknown Problem'),
			s.type,
			COALESCE(s.language, ''),
			s.status,
			s.created_at,
			COALESCE(s.runtime, 0),
			COALESCE(s.memory, 0)
		FROM submissions s
		LEFT JOIN users u ON s.user_id = u.id
		LEFT JOIN problems p ON s.problem_id = p.id
		WHERE s.id = $1
	`

	var details dto.AdminSubmissionDetails
	row := s.db.QueryRowContext(ctx, q, submissionID)
	if err := row.Scan(
		&details.ID,
		&details.UserID,
		&details.UserName,
		&details.UserEmail,
		&details.ContestID,
		&details.ProblemID,
		&details.ProblemName,
		&details.Type,
		&details.Language,
		&details.Status,
		&details.CreatedAt,
		&details.Runtime,
		&details.Memory,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, common.ErrNotFound
		}
		return nil, fmt.Errorf("scan admin submission details: %w", err)
	}

	testCaseResults, err := s.GetTestCaseResultsBySubmissionID(ctx, submissionID)
	if err != nil {
		log.Printf("submission-store: failed to get test case results for admin submission ID %s: %v", submissionID, err)
		details.TestCaseResults = []models.TestCaseResult{}
	} else {
		details.TestCaseResults = testCaseResults
	}

	return &details, nil
}

