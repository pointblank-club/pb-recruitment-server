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
	"time"

	"github.com/labstack/gommon/log"
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

func (ss *SubmissionService) JudgeMCQ(ctx context.Context, submissionID string) error {
	return ss.stores.Submissions.JudgeMCQ(ctx, submissionID)
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

	problem, err := ss.stores.Problems.GetProblem(ctx, req.ProblemID, req.ContestID)
	if err != nil {
		return "", err
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
			for id := range tokens {
				failedIDs = append(failedIDs, id)
			}
		}
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
