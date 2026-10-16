package services

import (
	"app/internal/common"
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/stores"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/gommon/log"
	"go.uber.org/fx"
)

type SubmissionService struct {
	stores *stores.Storage
	s3     *s3.S3
	judge0 *judge0.Client
}

func NewSubmissionService(stores *stores.Storage, s3 *s3.S3, judge0Client *judge0.Client) *SubmissionService {
	return &SubmissionService{stores: stores, s3: s3, judge0: judge0Client}
}

func (ss *SubmissionService) GetSubmissionStatusByID(ctx context.Context, id string) (*models.Submission, error) {
	sub, err := ss.stores.Submissions.GetSubmissionStatusByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return sub, nil
}

func (ss *SubmissionService) GetSubmissionDetailsByID(ctx context.Context, id string) (*dto.GetSubmissionDetailsResponse, error) {
	sub, err := ss.stores.Submissions.GetSubmissionDetailsByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if sub.Type == models.Code {
		sub.Code, err = ss.s3.GetObject(ctx, sub.ID)
		if err != nil {
			return nil, err
		}
	}
	return sub, nil
}

func (ss *SubmissionService) ListUserSubmissionsByProblemID(ctx context.Context, userID, problemID string, page int) ([]models.Submission, error) {
	sub, err := ss.stores.Submissions.ListUserSubmissionsByProblemID(ctx, userID, problemID, page)
	if err != nil {
		return nil, err
	}
	return sub, nil
}

func (ss *SubmissionService) CheckRateLimit(ctx context.Context, userID string) error {
	if ss == nil || ss.stores == nil || ss.stores.Submissions == nil {
		return nil
	}

	cooldown := int64(5) // default 5 seconds
	if envVal := os.Getenv("SUBMISSION_RATE_LIMIT_SECONDS"); envVal != "" {
		if val, err := strconv.ParseInt(envVal, 10, 64); err == nil {
			cooldown = val
		}
	}

	return ss.stores.Submissions.CheckSubmissionRateLimit(ctx, userID, cooldown)
}

func (ss *SubmissionService) CheckRateLimitWithCooldown(ctx context.Context, userID string, cooldown int64) error {
	if ss == nil || ss.stores == nil || ss.stores.Submissions == nil {
		return nil
	}
	return ss.stores.Submissions.CheckSubmissionRateLimit(ctx, userID, cooldown)
}

func (ss *SubmissionService) JudgeMCQ(ctx context.Context, submissionID string) error {
	if err := ss.stores.Submissions.JudgeMCQ(ctx, submissionID); err != nil {
		return ss.markSubmissionFailed(ctx, submissionID, err)
	}
	return nil
}

func (ss *SubmissionService) CreateSubmission(ctx context.Context, userID string, submissionType models.SubmissionType, req *dto.SubmitSubmissionRequest) (string, error) {
	ctx, cancelPreparation := context.WithTimeout(ctx, 5*time.Second)
	defer cancelPreparation()

	sub := &models.Submission{
		UserID:    userID,
		ContestID: req.ContestID,
		ProblemID: req.ProblemID,
		Type:      submissionType,
		Status:    models.Pending,
		Language:  req.Language,
		Option:    req.Option,
	}

	problem, err := ss.stores.Problems.GetProblem(ctx, req.ProblemID, req.ContestID)
	if err != nil {
		if errors.Is(err, common.ContestNotFoundError) {
			return "", common.ErrNotFound
		}
		return "", err
	}
	if problem.Type != submissionType {
		return "", common.ErrNotFound
	}
	if submissionType != models.Code {
		if err := ss.CheckRateLimit(ctx, userID); err != nil {
			return "", err
		}
		return ss.stores.Submissions.CreateSubmission(ctx, sub)
	}

	source, err := decodeSource(req.Code)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(source) == "" {
		return "", common.ErrInvalidCode
	}

	languageID, err := judge0.LanguageID(req.Language)
	if err != nil {
		return "", common.ErrUnsupportedLanguage
	}

	inputs, outputs, err := ss.loadTestcases(ctx, req.ContestID, req.ProblemID, problem.TestcasesKey)
	if err != nil {
		return "", err
	}
	if len(inputs) == 0 {
		return "", common.ErrNoTestcases
	}

	if err := ss.CheckRateLimit(ctx, userID); err != nil {
		return "", err
	}

	submissionID, err := ss.stores.Submissions.CreateSubmission(ctx, sub)
	if err != nil {
		return "", err
	}

	if err := ss.s3.PutObject(ctx, submissionID, req.Code); err != nil {
		return "", ss.markSubmissionFailed(ctx, submissionID, err)
	}

	indexes := make([]int, len(inputs))
	for i := range inputs {
		indexes[i] = i
	}

	cpuLimit, memLimit := judgeLimits(problem.TimeLimit, problem.MemoryLimit)
	compilerOptions := judge0.CompilerOptions(languageID)
	jobs := make([]judge0.SubmissionRequest, len(inputs))
	for i := range inputs {
		jobs[i] = judge0.SubmissionRequest{
			SourceCode: source, LanguageID: languageID, Stdin: inputs[i], ExpectedOutput: &outputs[i],
			CPUTimeLimit: cpuLimit, MemoryLimit: memLimit, CompilerOptions: compilerOptions,
		}
	}
	executions, err := ss.stores.Executions.InsertBatch(ctx, submissionID, indexes, jobs)
	if err != nil {
		return "", ss.markSubmissionFailed(ctx, submissionID, err)
	}
	for i, exec := range executions {
		jobs[i].CallbackURL = ss.judge0.CallbackURL(exec.ID, exec.Attempt)
	}

	dispatchCtx, cancelDispatch := context.WithTimeout(context.WithoutCancel(ctx), ss.judge0.Timeout())
	defer cancelDispatch()

	results, err := ss.judge0.CreateBatch(dispatchCtx, jobs)
	if err != nil {
		log.Errorf("judge0 batch failed for submission %s: %v", submissionID, err)
	}

	dbCtx, cancelDB := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelDB()
	for i, exec := range executions {
		if i >= len(results) {
			continue
		}
		result := results[i]
		if result.Token != "" {
			if err := ss.stores.Executions.SaveAttemptToken(dbCtx, exec.ID, exec.Attempt, result.Token); err != nil {
				log.Errorf("save Judge0 token for execution %s: %v", exec.ID, err)
			}
		} else if errors.Is(result.Error, judge0.ErrDispatchRejected) {
			if err := ss.stores.Executions.ProcessFinal(dbCtx, stores.FinalExecutionResult{ExecutionID: exec.ID, Attempt: exec.Attempt, Status: "judge_error"}); err != nil {
				return "", err
			}
		}
	}

	return submissionID, nil

}

func (ss *SubmissionService) HandleJudge0Callback(ctx context.Context, executionID string, payload judge0.CallbackResult) error {
	status, final := mapJudgeStatus(payload.Status.ID)
	if !final {
		if payload.Token == "" {
			return fmt.Errorf("missing Judge0 token")
		}
		return ss.stores.Executions.SaveAttemptToken(ctx, executionID, payload.Attempt, payload.Token)
	}
	runtime, err := judgeRuntimeMillis(payload.Time)
	if err != nil {
		return err
	}
	memory := int64(0)
	if payload.Memory != nil {
		memory = *payload.Memory
	}
	return ss.stores.Executions.ProcessFinal(ctx, stores.FinalExecutionResult{
		ExecutionID:   executionID,
		Attempt:       payload.Attempt,
		Stdout:        payload.Stdout,
		Token:         payload.Token,
		Status:        status,
		Runtime:       runtime,
		Memory:        memory,
		CompileOutput: judgeOutputText(payload.CompileOutput),
		Stderr:        judgeOutputText(payload.Stderr),
	})
}

const judgeOutputLimit = 4096

func judgeOutputText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	// Judge0 1.13.1 callbacks always Base64-encode output fields, and
	// recovery requests base64_encoded=true. Malformed data is discarded rather
	// than treated as plain text, which could corrupt valid-looking Base64.
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return truncateJudgeOutput(sanitizeJudgeOutput(string(decoded)))
}

// sanitizeJudgeOutput replaces invalid UTF-8 sequences with U+FFFD and
// strips NUL bytes that PostgreSQL TEXT columns reject (SQLSTATE 22021).
func sanitizeJudgeOutput(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\ufffd")
	}
	return strings.ReplaceAll(s, "\x00", "")
}

func truncateJudgeOutput(s string) string {
	if len(s) <= judgeOutputLimit {
		return s
	}
	s = s[:judgeOutputLimit]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func mapJudgeStatus(id int) (string, bool) {
	switch {
	case id <= 2:
		return "pending", false
	case id == 3:
		return "accepted", true
	case id == 4:
		return "wrong_answer", true
	case id == 5:
		return "tle", true
	case id == 6:
		return "failed_to_process", true
	case id >= 7 && id <= 12:
		return "rte", true
	case id == 13 || id == 14:
		return "judge_error", true
	default:
		return "", false
	}
}
func judgeRuntimeMillis(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var s string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, err
		}
	} else {
		s = string(raw)
	}
	seconds, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds >= float64(math.MaxInt64)/1000 {
		return 0, fmt.Errorf("negative Judge0 runtime")
	}
	return int64(seconds*1000 + 0.5), nil
}

const maxJudgeRetries = 3

func judgeLimits(milliseconds, mebibytes int) (float64, float64) {
	cpu, memory := float64(milliseconds)/1000, float64(mebibytes)*1024
	if cpu <= 0 {
		cpu = 1
	}
	if memory <= 0 {
		memory = 256 * 1024
	}
	return cpu, memory
}

func (ss *SubmissionService) RecoverJudge0(ctx context.Context) {
	if ss.stores.Submissions != nil {
		orphans, err := ss.stores.Submissions.PendingCodeWithoutExecutions(ctx, time.Now().Unix()-30, 50)
		if err != nil {
			log.Errorf("list unprepared submissions: %v", err)
		}
		for _, id := range orphans {
			if err := ss.stores.Submissions.MarkFailed(ctx, id); err != nil {
				log.Errorf("fail unprepared submission: %v", err)
			}
		}
	}
	completed, err := ss.stores.Executions.TerminalPendingParents(ctx, 50)
	if err != nil {
		log.Errorf("reconcile Judge0 parents: %v", err)
	}
	for _, result := range completed {
		if err := ss.stores.Executions.ProcessFinal(ctx, result); err != nil {
			log.Errorf("reconcile Judge0 result: %v", err)
		}
	}
	// Claim only as much work as this sweep can service before its lease expires.
	for ctx.Err() == nil {
		items, err := ss.stores.Executions.ClaimRecovery(ctx, 1)
		if err != nil {
			log.Errorf("claim Judge0 recovery: %v", err)
			return
		}
		if len(items) == 0 {
			return
		}
		e := items[0]
		healthy, err := ss.recoverExecution(ctx, e)
		if err != nil {
			log.Errorf("recover execution %s: %v", e.ID, err)
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if err := ss.stores.Executions.ReleaseRecovery(dbCtx, e, healthy); err != nil {
			log.Errorf("release recovery: %v", err)
		}
		cancel()
	}
}

func (ss *SubmissionService) recoverExecution(ctx context.Context, e models.Execution) (bool, error) {
	finalize := func(status string) error {
		return ss.stores.Executions.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: e.ID, Attempt: e.Attempt, Claim: e.Claim, Token: e.Judge0Token, Status: status})
	}
	callCtx, cancel := context.WithTimeout(ctx, ss.judge0.Timeout())
	defer cancel()
	if e.Judge0Token == "" {
		if e.DispatchCount > maxJudgeRetries {
			return false, finalize("judge_error")
		}
		job, err := ss.recoveryJob(callCtx, e)
		if err != nil {
			return false, err
		}
		// shortcut: a lost response and delayed callback can duplicate execution; use provider idempotency if CPU duplication becomes unacceptable.
		return ss.dispatchRecovery(callCtx, e, job)
	}
	results, err := ss.judge0.GetSubmissions(callCtx, []string{e.Judge0Token})
	if err != nil || results[0] == nil || results[0].Status.ID >= 13 {
		if e.RecoveryFailures >= maxJudgeRetries {
			return false, finalize("judge_error")
		}
		return false, err
	}
	result := results[0]
	status, final := mapJudgeStatus(result.Status.ID)
	// A real queued/running job is not evidence of abandonment.
	if !final {
		return true, nil
	}
	if status == "wrong_answer" && bytes.Equal(bytes.TrimSpace(result.Stdout), []byte("null")) && len(e.DispatchPayload) == 0 {
		if _, err := ss.recoveryJob(callCtx, e); err != nil {
			if e.RecoveryFailures >= maxJudgeRetries {
				return false, finalize("judge_error")
			}
			return false, err
		}
	}
	runtime, err := judgeRuntimeMillis(result.Time)
	if err != nil {
		if e.RecoveryFailures >= maxJudgeRetries {
			return false, finalize("judge_error")
		}
		return false, err
	}
	memory := int64(0)
	if result.Memory != nil {
		memory = *result.Memory
	}
	return true, ss.stores.Executions.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: e.ID, Attempt: e.Attempt, Claim: e.Claim, Token: result.Token, Status: status, Stdout: result.Stdout, Runtime: runtime, Memory: memory, CompileOutput: judgeOutputText(result.CompileOutput), Stderr: judgeOutputText(result.Stderr)})
}

func (ss *SubmissionService) dispatchRecovery(ctx context.Context, e models.Execution, job judge0.SubmissionRequest) (bool, error) {
	job.CallbackURL = ss.judge0.CallbackURL(e.ID, e.Attempt)
	results, err := ss.judge0.CreateBatch(ctx, []judge0.SubmissionRequest{job})
	if len(results) == 1 && results[0].Token != "" {
		return true, ss.stores.Executions.SaveAttemptToken(ctx, e.ID, e.Attempt, results[0].Token)
	}
	if len(results) == 1 && errors.Is(results[0].Error, judge0.ErrDispatchRejected) {
		return false, ss.stores.Executions.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: e.ID, Attempt: e.Attempt, Claim: e.Claim, Token: e.Judge0Token, Status: "judge_error"})
	}
	return false, err
}

func (ss *SubmissionService) recoveryJob(ctx context.Context, e models.Execution) (judge0.SubmissionRequest, error) {
	var job judge0.SubmissionRequest
	if len(e.DispatchPayload) > 0 {
		if err := json.Unmarshal(e.DispatchPayload, &job); err != nil {
			return job, err
		}
	} else {
		// Legacy rows and crashes before execution preparation have no snapshot yet.
		sub, err := ss.GetSubmissionDetailsByID(ctx, e.SubmissionID)
		if err != nil {
			return job, err
		}
		problem, err := ss.stores.Problems.GetProblem(ctx, sub.ProblemID, sub.ContestID)
		if err != nil {
			return job, err
		}
		inputs, outputs, err := ss.loadTestcases(ctx, sub.ContestID, sub.ProblemID, problem.TestcasesKey)
		if err != nil {
			return job, err
		}
		if e.TestCaseIndex < 0 || e.TestCaseIndex >= len(inputs) {
			return job, common.ErrNoTestcases
		}
		source, err := decodeSource(sub.Code)
		if err != nil {
			return job, err
		}
		language, err := judge0.LanguageID(sub.Language)
		if err != nil {
			return job, err
		}
		cpu, memory := judgeLimits(problem.TimeLimit, problem.MemoryLimit)
		job = judge0.SubmissionRequest{SourceCode: source, LanguageID: language, Stdin: inputs[e.TestCaseIndex], ExpectedOutput: &outputs[e.TestCaseIndex], CPUTimeLimit: cpu, MemoryLimit: memory, CompilerOptions: judge0.CompilerOptions(language)}
		if err := ss.stores.Executions.SaveRecoveryPayload(ctx, e, job); err != nil {
			return job, err
		}
	}
	return job, nil
}

func NewJudge0Recovery(lc fx.Lifecycle, ss *SubmissionService) {
	workerCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{OnStart: func(context.Context) error {
		go func() {
			defer close(done)
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-workerCtx.Done():
					return
				case <-ticker.C:
					ctx, cancel := context.WithTimeout(workerCtx, 30*time.Second)
					ss.RecoverJudge0(ctx)
					cancel()
				}
			}
		}()
		return nil
	}, OnStop: func(ctx context.Context) error {
		stop()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
}

func (ss *SubmissionService) markSubmissionFailed(ctx context.Context, submissionID string, cause error) error {
	failureCtx, cancelFailure := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelFailure()
	if err := ss.stores.Submissions.MarkFailed(failureCtx, submissionID); err != nil {
		log.Errorf("mark failed submission %s: %v", submissionID, err)
		return errors.Join(cause, err)
	}
	return cause
}

func (ss *SubmissionService) loadTestcases(ctx context.Context, contestID, problemID, testcasesKey string) ([]string, []string, error) {
	if testcasesKey == "" {
		testcasesKey = fmt.Sprintf("problems/%s/%s/testcases.json", contestID, problemID)
	}
	answersKey := fmt.Sprintf("problems/%s/%s/answers.json", contestID, problemID)

	tcRaw, err := ss.s3.GetObject(ctx, testcasesKey)
	if err != nil {
		return nil, nil, mapObjectReadError(err)
	}
	ansRaw, err := ss.s3.GetObject(ctx, answersKey)
	if err != nil {
		return nil, nil, mapObjectReadError(err)
	}

	var cases []struct {
		Index int    `json:"index"`
		Input string `json:"input"`
	}
	if err := json.Unmarshal([]byte(tcRaw), &cases); err != nil {
		return nil, nil, common.ErrNoTestcases
	}

	var answers []string
	if err := json.Unmarshal([]byte(ansRaw), &answers); err != nil {
		return nil, nil, common.ErrNoTestcases
	}
	if len(cases) == 0 || len(cases) != len(answers) {
		return nil, nil, common.ErrNoTestcases
	}

	inputs := make([]string, len(cases))
	outputs := make([]string, len(cases))
	for i, tc := range cases {
		idx := tc.Index
		if idx < 0 || idx >= len(cases) {
			idx = i
		}
		inputs[idx] = tc.Input
		outputs[idx] = answers[idx]
	}
	return inputs, outputs, nil
}

func decodeSource(code string) (string, error) {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return "", common.ErrInvalidCode
	}
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return code, nil
	}
	return string(decoded), nil
}

func mapObjectReadError(err error) error {
	if errors.Is(err, common.KeyNotFoundError) {
		return common.ErrNoTestcases
	}
	return err
}
