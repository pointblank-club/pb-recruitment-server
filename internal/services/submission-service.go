package services

import (
	"app/internal/common"
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/stores"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/labstack/gommon/log"
	"go.uber.org/fx"
	"strconv"
)

type SubmissionService struct {
	stores             *stores.Storage
	s3                 *s3.S3
	judge0             *judge0.Client
	recoveryCursorTime int64
	recoveryCursorID   string
	cursorMu           sync.Mutex
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

	executions, err := ss.stores.Executions.InsertBatch(ctx, submissionID, indexes)
	if err != nil {
		return "", ss.markSubmissionFailed(ctx, submissionID, err)
	}

	cpuLimit := float64(problem.TimeLimit) / 1000.0
	if cpuLimit <= 0 {
		cpuLimit = 1
	}
	memLimit := float64(problem.MemoryLimit) * 1024
	if memLimit <= 0 {
		memLimit = 256 * 1024
	}

	compilerOptions := judge0.CompilerOptions(languageID)
	jobs := make([]judge0.SubmissionRequest, len(executions))
	for i, exec := range executions {
		jobs[i] = judge0.SubmissionRequest{
			SourceCode:      source,
			LanguageID:      languageID,
			Stdin:           inputs[i],
			ExpectedOutput:  outputs[i],
			CPUTimeLimit:    cpuLimit,
			MemoryLimit:     memLimit,
			CallbackURL:     ss.judge0.CallbackURL(exec.ID),
			CompilerOptions: compilerOptions,
		}
	}

	dispatchCtx, cancelDispatch := context.WithTimeout(context.WithoutCancel(ctx), ss.judge0.Timeout())
	defer cancelDispatch()

	results, err := ss.judge0.CreateBatch(dispatchCtx, jobs)
	if err != nil {
		log.Errorf("judge0 batch failed for submission %s: %v", submissionID, err)
	}

	tokens, failedIDs := classifyDispatchResults(executions, results)

	dbCtx, cancelDB := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelDB()

	if len(tokens) > 0 {
		if saveErr := ss.stores.Executions.SaveTokens(dbCtx, tokens); saveErr != nil {
			log.Errorf("save judge0 tokens for submission %s: %v", submissionID, saveErr)
			if !ss.judge0.CallbacksEnabled() {
				for id := range tokens {
					failedIDs = append(failedIDs, id)
				}
			}
		}
	}

	if ss.judge0.CallbacksEnabled() {
		refusedIDs := make([]string, 0)
		for i, exec := range executions {
			if i < len(results) && results[i].Error != nil {
				if errors.Is(results[i].Error, judge0.ErrDispatchRejected) || errors.Is(results[i].Error, judge0.ErrInvalidResponse) {
					refusedIDs = append(refusedIDs, exec.ID)
				}
			}
		}
		failedIDs = refusedIDs
	}

	if len(failedIDs) > 0 {
		failureCtx, cancelFailure := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelFailure()
		if markErr := ss.stores.Executions.MarkFailed(failureCtx, failedIDs); markErr != nil {
			log.Errorf("mark failed executions for submission %s: %v", submissionID, markErr)
			return "", markErr
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
		return ss.stores.Executions.BindToken(ctx, executionID, payload.Token)
	}
	runtime, err := judgeRuntimeMillis(payload.Time)
	if err != nil {
		return err
	}
	memory := int64(0)
	if payload.Memory != nil {
		memory = *payload.Memory
	}
	return ss.stores.Executions.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: executionID, Token: payload.Token, Status: status, Runtime: runtime, Memory: memory})
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
	if seconds < 0 {
		return 0, fmt.Errorf("negative Judge0 runtime")
	}
	return int64(seconds*1000 + 0.5), nil
}

func (ss *SubmissionService) RecoverJudge0(ctx context.Context) {
	if !ss.cursorMu.TryLock() {
		return
	}
	defer ss.cursorMu.Unlock()

	if ss.stores.Submissions != nil {
		orphans, orphanErr := ss.stores.Submissions.PendingCodeWithoutExecutions(ctx, time.Now().Unix()-10, 50)
		if orphanErr != nil {
			log.Errorf("judge0 recovery list unprepared submissions failed: %v", orphanErr)
		} else {
			for _, id := range orphans {
				if err := ss.stores.Submissions.MarkFailed(ctx, id); err != nil {
					log.Errorf("judge0 recovery fail unprepared submission %s: %v", id, err)
				}
			}
		}
	}

	// Allow late callbacks after the bounded dispatch path before failing orphaned jobs.
	staleIDs, err := ss.stores.Executions.StaleTokenlessExecutions(ctx, 300, 50)
	if err != nil {
		log.Errorf("judge0 recovery list stale tokenless failed: %v", err)
	} else if len(staleIDs) > 0 && !ss.judge0.CallbacksEnabled() {
		if err := ss.stores.Executions.MarkFailed(ctx, staleIDs); err != nil {
			log.Errorf("judge0 recovery cleanup stale tokenless failed: %v", err)
		}
	}

	completed, err := ss.stores.Executions.TerminalPendingParents(ctx, 50)
	if err != nil {
		log.Errorf("judge0 recovery reconcile failed: %v", err)
	} else {
		for _, result := range completed {
			if ctx.Err() != nil {
				return
			}
			if e := ss.stores.Executions.ProcessFinal(ctx, result); e != nil {
				log.Errorf("judge0 recovery reconcile result failed: %v", e)
			}
		}
	}

	items, err := ss.stores.Executions.PendingWithTokens(ctx, 50, ss.recoveryCursorTime, ss.recoveryCursorID)
	if err != nil {
		log.Errorf("judge0 recovery list failed: %v", err)
		return
	}

	if len(items) == 0 {
		ss.recoveryCursorTime = 0
		ss.recoveryCursorID = ""
		return
	}

	for _, item := range items {
		if ctx.Err() != nil {
			log.Warnf("judge0 recovery context timeout, breaking early")
			break
		}
		callCtx, cancel := context.WithTimeout(ctx, ss.judge0.Timeout())
		result, e := ss.judge0.GetSubmission(callCtx, item.Judge0Token)
		cancel()
		// Advance only past attempted jobs so a timeout cannot skip the rest of the page.
		ss.recoveryCursorTime, ss.recoveryCursorID = item.CreatedAt, item.ID
		if errors.Is(e, judge0.ErrSubmissionNotFound) {
			if e = ss.stores.Executions.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: item.ID, Token: item.Judge0Token, Status: "judge_error"}); e != nil {
				log.Errorf("judge0 recovery missing token finalization failed: %v", e)
			}
			continue
		}
		if e != nil || result == nil {
			continue
		}
		_, final := mapJudgeStatus(result.Status.ID)
		if !final {
			continue
		}
		payload := judge0.CallbackResult{Token: result.Token, Time: result.Time, Memory: result.Memory}
		payload.Status.ID = result.Status.ID
		if payload.Token == "" {
			payload.Token = item.Judge0Token
		}
		if e = ss.HandleJudge0Callback(ctx, item.ID, payload); e != nil {
			log.Errorf("judge0 recovery result failed: %v", e)
		}
	}
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

func classifyDispatchResults(executions []models.Execution, results []judge0.SubmissionResult) (map[string]string, []string) {
	tokens := map[string]string{}
	failedIDs := make([]string, 0)
	for i, exec := range executions {
		if i < len(results) && results[i].Error == nil && results[i].Token != "" {
			tokens[exec.ID] = results[i].Token
		} else {
			failedIDs = append(failedIDs, exec.ID)
		}
	}
	return tokens, failedIDs
}

func mapObjectReadError(err error) error {
	if errors.Is(err, common.KeyNotFoundError) {
		return common.ErrNoTestcases
	}
	return err
}
