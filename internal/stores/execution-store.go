package stores

import (
	"app/internal/models"
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type FinalExecutionResult struct {
	ExecutionID, Token, Status string
	Runtime, Memory            int64
}
type executionChild struct{ id, status string }

type ExecutionStore struct {
	db *sql.DB
}

func NewExecutionStore(db *sql.DB) *ExecutionStore {
	return &ExecutionStore{db: db}
}

func (s *ExecutionStore) InsertBatch(ctx context.Context, submissionID string, indexes []int) ([]models.Execution, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("execution store: db is not initialized")
	}
	if len(indexes) == 0 {
		return []models.Execution{}, nil
	}

	now := time.Now().Unix()
	executions := make([]models.Execution, len(indexes))
	ids := make([]string, len(indexes))
	created := make([]int64, len(indexes))

	for i, idx := range indexes {
		id := uuid.NewString()
		ids[i] = id
		created[i] = now
		executions[i] = models.Execution{
			ID:            id,
			SubmissionID:  submissionID,
			TestCaseIndex: idx,
			Status:        "pending",
			CreatedAt:     now,
		}
	}

	const q = `
		INSERT INTO submission_executions (id, submission_id, test_case_index, status, created_at)
		SELECT unnest($1::uuid[]), $2, unnest($3::int[]), 'pending', unnest($4::bigint[])
	`

	_, err := s.db.ExecContext(ctx, q, pq.Array(ids), submissionID, pq.Array(indexes), pq.Array(created))
	if err != nil {
		return nil, fmt.Errorf("insert executions: %w", err)
	}

	return executions, nil
}

func (s *ExecutionStore) SaveTokens(ctx context.Context, tokens map[string]string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("execution store: db is not initialized")
	}
	if len(tokens) == 0 {
		return nil
	}

	ids := make([]string, 0, len(tokens))
	values := make([]string, 0, len(tokens))
	for id, token := range tokens {
		ids = append(ids, id)
		values = append(values, token)
	}
	order := make([]int, len(ids))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return ids[order[i]] < ids[order[j]] })
	sortedIDs := make([]string, len(ids))
	sortedValues := make([]string, len(values))
	for i, j := range order {
		sortedIDs[i] = ids[j]
		sortedValues[i] = values[j]
	}
	ids, values = sortedIDs, sortedValues

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT submission_id FROM submission_executions WHERE id=ANY($1::uuid[]) ORDER BY submission_id`, pq.Array(ids))
	if err != nil {
		return err
	}
	var parents []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		parents = append(parents, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, parent := range parents {
		var locked string
		if err = tx.QueryRowContext(ctx, `SELECT id FROM submissions WHERE id=$1 FOR UPDATE`, parent).Scan(&locked); err != nil {
			return err
		}
	}
	for i, id := range ids {
		var status, token string
		err = tx.QueryRowContext(ctx, `SELECT status, COALESCE(judge0_token,'') FROM submission_executions WHERE id=$1 FOR UPDATE`, id).Scan(&status, &token)
		if err != nil {
			return err
		}
		if token != "" && token != values[i] {
			return fmt.Errorf("conflicting Judge0 token for execution %s", id)
		}
		if token == "" {
			_, err = tx.ExecContext(ctx, `UPDATE submission_executions SET judge0_token=$2 WHERE id=$1`, id, values[i])
			if err != nil {
				return err
			}
		}
	}
	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("save judge0 tokens: %w", err)
	}
	return nil
}

func (s *ExecutionStore) MarkFailed(ctx context.Context, ids []string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("execution store: db is not initialized")
	}
	if len(ids) == 0 {
		return nil
	}

	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT submission_id FROM submission_executions WHERE id=ANY($1::uuid[]) ORDER BY submission_id`, pq.Array(ids))
	if err != nil {
		return err
	}
	var parents []string
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		parents = append(parents, p)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, parent := range parents {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		var status string
		e = tx.QueryRowContext(ctx, `SELECT status FROM submissions WHERE id=$1 FOR UPDATE`, parent).Scan(&status)
		if e != nil {
			tx.Rollback()
			return e
		}
		execs, e := tx.QueryContext(ctx, `SELECT id, status, test_case_index FROM submission_executions WHERE submission_id=$1 ORDER BY test_case_index FOR UPDATE`, parent)
		if e != nil {
			tx.Rollback()
			return e
		}
		type child struct {
			id            string
			status        string
			testCaseIndex int
		}
		var children []child
		for execs.Next() {
			var c child
			if e = execs.Scan(&c.id, &c.status, &c.testCaseIndex); e != nil {
				execs.Close()
				tx.Rollback()
				return e
			}
			children = append(children, c)
		}
		if e = execs.Err(); e != nil {
			execs.Close()
			tx.Rollback()
			return e
		}
		execs.Close()
		for _, c := range children {
			for _, id := range ids {
				if c.id == id && c.status == "pending" {
					res, e := tx.ExecContext(ctx, `UPDATE submission_executions SET status='judge_error',runtime=0,memory=0 WHERE id=$1 AND status='pending' AND judge0_token IS NULL`, id)
					if e != nil {
						tx.Rollback()
						return e
					}

					rowsAffected, e := res.RowsAffected()
					if e != nil {
						tx.Rollback()
						return e
					}

					if rowsAffected > 0 {
						_, e = tx.ExecContext(ctx, `INSERT INTO test_case_results(id,execution_id,submission_id,test_case_id,status,runtime,memory,created_at) VALUES($1,$1,$2,$3,'judge_error',0,0,$4) ON CONFLICT(execution_id) DO NOTHING`, id, parent, fmt.Sprint(c.testCaseIndex), time.Now().Unix())
						if e != nil {
							tx.Rollback()
							return e
						}
					}
				}
			}
		}
		if status == "pending" {
			var pending bool
			e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM submission_executions WHERE submission_id=$1 AND status='pending')`, parent).Scan(&pending)
			if e != nil {
				tx.Rollback()
				return e
			}
			if !pending {
				_, e = tx.ExecContext(ctx, `UPDATE submissions s SET status=CASE
					WHEN EXISTS(SELECT 1 FROM submission_executions WHERE submission_id=$1 AND status='failed_to_process') THEN 'failed_to_process'::submission_status
					WHEN EXISTS(SELECT 1 FROM submission_executions WHERE submission_id=$1 AND status='wrong_answer') THEN 'wrong_answer'::submission_status
					WHEN EXISTS(SELECT 1 FROM submission_executions WHERE submission_id=$1 AND status='tle') THEN 'tle'::submission_status
					WHEN EXISTS(SELECT 1 FROM submission_executions WHERE submission_id=$1 AND status='rte') THEN 'rte'::submission_status
					ELSE 'judge_error'::submission_status END,
					runtime=COALESCE((SELECT sum(runtime) FROM submission_executions WHERE submission_id=$1),0),
					memory=COALESCE((SELECT max(memory) FROM submission_executions WHERE submission_id=$1),0)
					WHERE s.id=$1 AND s.status='pending'`, parent)
				if e != nil {
					tx.Rollback()
					return e
				}
			}
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	return nil
}

func (s *ExecutionStore) ProcessFinal(ctx context.Context, r FinalExecutionResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var subID, userID, contestID, problemID, subStatus string
	if err = tx.QueryRowContext(ctx, `SELECT s.id,s.user_id,s.contest_id,s.problem_id,s.status FROM submissions s JOIN submission_executions e ON e.submission_id=s.id WHERE e.id=$1 FOR UPDATE OF s`, r.ExecutionID).Scan(&subID, &userID, &contestID, &problemID, &subStatus); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,status FROM submission_executions WHERE submission_id=$1 ORDER BY id FOR UPDATE`, subID)
	if err != nil {
		return err
	}
	var children []executionChild
	for rows.Next() {
		var c executionChild
		if err = rows.Scan(&c.id, &c.status); err != nil {
			rows.Close()
			return err
		}
		children = append(children, c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var currentStatus, currentToken string
	var index int
	var storedRuntime, storedMemory int64
	if err = tx.QueryRowContext(ctx, `SELECT status,COALESCE(judge0_token,''),test_case_index,COALESCE(runtime,0),COALESCE(memory,0) FROM submission_executions WHERE id=$1`, r.ExecutionID).Scan(&currentStatus, &currentToken, &index, &storedRuntime, &storedMemory); err != nil {
		return err
	}
	if currentToken != "" && r.Token != "" && currentToken != r.Token {
		return fmt.Errorf("conflicting Judge0 token")
	}
	alreadyTerminal := currentStatus != "pending"
	if subStatus != "pending" {
		return tx.Commit()
	}
	if alreadyTerminal {
		r.Status = currentStatus
		r.Runtime, r.Memory = storedRuntime, storedMemory
	}
	if !alreadyTerminal && r.Token != "" {
		_, err = tx.ExecContext(ctx, `UPDATE submission_executions SET judge0_token=$2 WHERE id=$1 AND (judge0_token IS NULL OR judge0_token=$2)`, r.ExecutionID, r.Token)
		if err != nil {
			return err
		}
	}
	if !alreadyTerminal {
		_, err = tx.ExecContext(ctx, `UPDATE submission_executions SET status=$2,runtime=$3,memory=$4 WHERE id=$1 AND status='pending'`, r.ExecutionID, r.Status, r.Runtime, r.Memory)
		if err != nil {
			return err
		}
	}
	conflict := `ON CONFLICT(execution_id) DO UPDATE SET status=EXCLUDED.status,runtime=EXCLUDED.runtime,memory=EXCLUDED.memory`
	if alreadyTerminal {
		conflict = `ON CONFLICT(execution_id) DO NOTHING`
	}

	tcStatus := r.Status
	if tcStatus == "accepted" {
		tcStatus = "pass"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO test_case_results(id,execution_id,submission_id,test_case_id,status,runtime,memory,created_at) VALUES($1,$1,$2,$3,$4,$5,$6,$7) `+conflict, r.ExecutionID, subID, fmt.Sprint(index), tcStatus, r.Runtime, r.Memory, time.Now().Unix())
	if err != nil {
		return err
	}
	var pending, accepted bool
	var totalRuntime, totalMemory int64
	err = tx.QueryRowContext(ctx, `SELECT bool_or(status='pending'),bool_and(status='accepted'),COALESCE(sum(runtime),0),COALESCE(max(memory),0) FROM submission_executions WHERE submission_id=$1`, subID).Scan(&pending, &accepted, &totalRuntime, &totalMemory)
	if err != nil {
		return err
	}
	if !pending {
		// Reconciliation must restore every missing result before completing the parent.
		_, err = tx.ExecContext(ctx, `INSERT INTO test_case_results(id,execution_id,submission_id,test_case_id,status,runtime,memory,created_at)
			SELECT id,id,submission_id,test_case_index::text,
			(CASE WHEN status='accepted' THEN 'pass' ELSE status END)::test_case_status,
			COALESCE(runtime,0),COALESCE(memory,0),$2
			FROM submission_executions WHERE submission_id=$1 AND status<>'pending'
			ON CONFLICT(execution_id) DO NOTHING`, subID, time.Now().Unix())
		if err != nil {
			return err
		}
		verdict := aggregateFailure(children, r.ExecutionID, r.Status)
		if accepted {
			verdict = "accepted"
		}
		_, err = tx.ExecContext(ctx, `UPDATE submissions SET status=$2,runtime=$3,memory=$4 WHERE id=$1 AND status='pending'`, subID, verdict, totalRuntime, totalMemory)
		if err != nil {
			return err
		}
		if verdict == "accepted" {
			_, err = tx.ExecContext(ctx, `INSERT INTO rankings(contest_id,user_id,score,hidden,disqualified) VALUES($1,$2,0,false,false) ON CONFLICT(contest_id,user_id) DO NOTHING`, contestID, userID)
			if err != nil {
				return err
			}
			var rankID string
			err = tx.QueryRowContext(ctx, `SELECT user_id FROM rankings WHERE contest_id=$1 AND user_id=$2 FOR UPDATE`, contestID, userID).Scan(&rankID)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE rankings SET score=COALESCE((SELECT sum(p.score) FROM problems p WHERE p.contest_id=$1 AND EXISTS(SELECT 1 FROM submissions x WHERE x.contest_id=p.contest_id AND x.problem_id=p.id AND x.user_id=$2 AND x.status='accepted')),0) WHERE contest_id=$1 AND user_id=$2`, contestID, userID)
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *ExecutionStore) BindToken(ctx context.Context, executionID, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var parent string
	if err = tx.QueryRowContext(ctx, `SELECT submission_id FROM submission_executions WHERE id=$1`, executionID).Scan(&parent); err != nil {
		return err
	}
	var ignored string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM submissions WHERE id=$1 FOR UPDATE`, parent).Scan(&ignored); err != nil {
		return err
	}
	var existing string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(judge0_token,'') FROM submission_executions WHERE id=$1 FOR UPDATE`, executionID).Scan(&existing); err != nil {
		return err
	}
	if existing != "" && existing != token {
		return fmt.Errorf("conflicting Judge0 token")
	}
	if existing == "" {
		if _, err = tx.ExecContext(ctx, `UPDATE submission_executions SET judge0_token=$2 WHERE id=$1`, executionID, token); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func aggregateFailure(children []executionChild, changedID, changedStatus string) string {
	// Precedence: compilation, wrong answer, time limit, runtime, judge error.
	for _, want := range []string{"failed_to_process", "wrong_answer", "tle", "rte", "judge_error"} {
		if changedID != "" && changedStatus == want {
			return want
		}
		for _, c := range children {
			if c.status == want {
				return want
			}
		}
	}
	return "judge_error"
}

func (s *ExecutionStore) PendingWithTokens(ctx context.Context, limit int, cursorTime int64, cursorID string) ([]models.Execution, error) {
	if cursorID == "" {
		cursorID = uuid.Nil.String()
	}
	query := `SELECT id::text, judge0_token, created_at FROM submission_executions
              WHERE status='pending' AND judge0_token IS NOT NULL
                AND created_at <= extract(epoch from now())::bigint - 30
                AND (created_at, id) > ($2, $3)
              ORDER BY created_at, id LIMIT $1`
	rows, err := s.db.QueryContext(ctx, query, limit, cursorTime, cursorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.Execution{}
	for rows.Next() {
		var x models.Execution
		if err = rows.Scan(&x.ID, &x.Judge0Token, &x.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *ExecutionStore) TerminalPendingParents(ctx context.Context, limit int) ([]FinalExecutionResult, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT ON (s.id) e.id::text,e.status,COALESCE(e.runtime,0),COALESCE(e.memory,0)
		FROM submission_executions e JOIN submissions s ON s.id=e.submission_id
		WHERE s.status='pending' AND e.status<>'pending'
		AND NOT EXISTS (SELECT 1 FROM submission_executions pending WHERE pending.submission_id=s.id AND pending.status='pending')
		ORDER BY s.id,e.created_at,e.id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FinalExecutionResult{}
	for rows.Next() {
		var r FinalExecutionResult
		if err = rows.Scan(&r.ExecutionID, &r.Status, &r.Runtime, &r.Memory); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *ExecutionStore) StaleTokenlessExecutions(ctx context.Context, olderThanSeconds int64, limit int) ([]string, error) {
	cutoff := time.Now().Unix() - olderThanSeconds
	rows, err := s.db.QueryContext(ctx, `SELECT id::text FROM submission_executions WHERE status='pending' AND judge0_token IS NULL AND created_at <= $1 ORDER BY created_at,id LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
