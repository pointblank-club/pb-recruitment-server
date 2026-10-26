package services

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/stores"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"fmt"

	"github.com/google/uuid"

	"github.com/labstack/gommon/log"
)

type ContestService struct {
	stores *stores.Storage
	s3     *s3.S3
}

func NewContestService(stores *stores.Storage, s3Client *s3.S3) *ContestService {
	return &ContestService{
		stores: stores,
		s3:     s3Client,
	}
}

func (cs *ContestService) CreateContest(ctx context.Context, contest *models.Contest) (*models.Contest, error) {
	if err := cs.stores.Contests.CreateContest(ctx, contest); err != nil {
		return nil, err
	}
	return contest, nil
}

func (cs *ContestService) UpdateContest(ctx context.Context, contest *models.Contest) (*models.Contest, error) {
	if err := cs.stores.Contests.UpdateContest(ctx, contest); err != nil {
		return nil, err
	}
	return contest, nil
}

func (cs *ContestService) DeleteContest(ctx context.Context, contestID string) error {
	return cs.stores.Contests.DeleteContest(ctx, contestID)
}

func (cs *ContestService) RegisterParticipant(contestID string, userID string) error {
	// Registration logic would go here
	return nil
}

func (cs *ContestService) ModifyRegistration(ctx context.Context, contestID string, userID string, action dto.RegisterationAction) error {
	contest, err := cs.stores.Contests.GetContest(ctx, contestID)
	if err != nil {
		return err
	}

	if contest.GetRegistrationStatus() != models.ContestRegistrationOpen {
		log.Errorf("contest %s is not open for registration", contestID)
		return common.ContestRegistrationClosedError
	}

	switch action {
	case dto.RegisterAction:
		user, err := cs.stores.Users.GetUserProfile(ctx, userID)
		if err != nil {
			log.Errorf("failed to get user profile for user %s: %v", userID, err)
			return err
		}

		if !slices.Contains(contest.EligibleTo, user.CurrentYear) {
			log.Errorf("user %s is not eligible to contest %s", userID, contestID)
			return common.InvalidYearError
		}

		return cs.stores.Contests.RegisterUser(ctx, contestID, userID)

	case dto.UnregisterAction:
		return cs.stores.Contests.UnregisterUser(ctx, contestID, userID)

	default:
		return fmt.Errorf("invalid action: %s", action)
	}
}

func (cs *ContestService) ListContests(ctx context.Context, page int) ([]models.Contest, error) {
	return cs.stores.Contests.ListContests(ctx, page)
}

//Problem Reated Services

func (cs *ContestService) CreateProblem(ctx context.Context, contestID string, req *dto.CreateProblemRequest) (*models.Problem, error) {
	if err := validateMCQAnswerRange(req); err != nil {
		return nil, err
	}

	problem := &models.Problem{
		ID:                 uuid.NewString(),
		ContestID:          contestID,
		Name:               req.Name,
		Score:              req.Score,
		Type:               req.Type,
		Answer:             req.Answer,
		Options:            req.Options,
		HasMultipleAnswers: req.Type == "mcq" && len(req.Answer) > 1,
		TimeLimit:          req.TimeLimit,
		MemoryLimit:        req.MemoryLimit,
	}

	s3Key := fmt.Sprintf("problems/%s/%s/description.json", contestID, problem.ID)

	payload := map[string]string{
		"description": req.Description,
	}

	data, _ := json.Marshal(payload)

	if err := cs.s3.PutObject(ctx, s3Key, string(data)); err != nil {
		return nil, err
	}

	problem.Description = s3Key

	if req.Type == "code" {

		testcasesKey := fmt.Sprintf("problems/%s/%s/testcases.json", contestID, problem.ID)
		if err := cs.writeTestcases(ctx, testcasesKey, req.Testcases); err != nil {
			return nil, err
		}

		problem.Testcases = testcasesKey
	}

	if problem.Options == nil {
		problem.Options = []string{}
	}

	if err := cs.stores.Problems.CreateProblem(ctx, problem); err != nil {
		return nil, err
	}

	return problem, nil
}

func (cs *ContestService) UpdateProblem(ctx context.Context, contestID string, problemID string, req *dto.CreateProblemRequest) (*models.Problem, error) {
	if err := validateMCQAnswerRange(req); err != nil {
		return nil, err
	}
	if err := cs.checkProblemEditable(ctx, contestID); err != nil {
		return nil, err
	}

	meta, err := cs.stores.Problems.GetProblem(ctx, problemID, contestID)
	if err != nil {
		return nil, err
	}

	// Use existing S3 key if it's valid, otherwise create a new one
	s3Key := meta.Description
	if s3Key == "" || !strings.HasPrefix(s3Key, "problems/") {
		// Create new S3 key for NULL/empty descriptions or legacy direct descriptions
		s3Key = fmt.Sprintf("problems/%s/%s/description.json", contestID, problemID)
	}

	payload := map[string]string{
		"description": req.Description,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	if err := cs.s3.PutObjectOverwrite(ctx, s3Key, string(data)); err != nil {
		return nil, err
	}

	// An empty key leaves the current database reference unchanged.
	var testcasesKey string

	if req.Type == "code" && len(req.Testcases) > 0 {

		// shortcut: retain old versions until problem deletion; add pruning if authoring churn warrants it.
		testcasesKey = fmt.Sprintf("problems/%s/%s/%s/testcases.json", contestID, problemID, uuid.NewString())
		if err := cs.writeTestcases(ctx, testcasesKey, req.Testcases); err != nil {
			return nil, err
		}
	}

	hasMultiple := req.Type == "mcq" && len(req.Answer) > 1

	problem := &models.Problem{
		ID:                 problemID,
		ContestID:          contestID,
		Name:               req.Name,
		Description:        s3Key,
		Score:              req.Score,
		Type:               req.Type,
		Answer:             req.Answer,
		Options:            req.Options,
		HasMultipleAnswers: hasMultiple,
		Testcases:          testcasesKey,
		TimeLimit:          req.TimeLimit,
		MemoryLimit:        req.MemoryLimit,
	}

	if problem.Options == nil {
		problem.Options = []string{}
	}
	preserveProblemLimits(problem, meta)
	if err := cs.stores.Problems.UpdateProblem(ctx, problem); err != nil {
		return nil, err
	}

	return problem, nil
}

func (cs *ContestService) writeTestcases(ctx context.Context, key string, cases []dto.CreateTestCaseRequest) error {
	inputs := make([]map[string]any, len(cases))
	answers := make([]string, len(cases))
	for i, tc := range cases {
		inputs[i] = map[string]any{"index": i, "input": tc.Input, "is_sample": tc.IsSample}
		answers[i] = tc.ExpectedOutput
	}
	inputJSON, _ := json.Marshal(inputs)
	answerJSON, _ := json.Marshal(answers)
	if err := cs.s3.PutObject(ctx, key, string(inputJSON)); err != nil {
		return err
	}
	return cs.s3.PutObject(ctx, testcaseAnswersKey(key), string(answerJSON))
}

func (cs *ContestService) DeleteProblem(ctx context.Context, contestID string, problemID string) error {
	if err := cs.checkProblemEditable(ctx, contestID); err != nil {
		return err
	}

	_, err := cs.stores.Problems.GetProblem(ctx, problemID, contestID)
	if err != nil {
		return err
	}

	if err := cs.stores.Problems.DeleteProblem(ctx, contestID, problemID); err != nil {
		return err
	}

	prefix := fmt.Sprintf("problems/%s/%s/", contestID, problemID)
	if err := cs.s3.DeletePrefix(ctx, prefix); err != nil {
		log.Errorf("failed to delete S3 folder for problem %s: %v", problemID, err)
	}

	// Pre-PR#42 rows stored the description at a flat key outside the prefix above.
	// Still swept for those; removable once no problem row predates the nested layout.
	objectKey := fmt.Sprintf("problems/%s/%s.json", contestID, problemID)
	if err := cs.s3.DeleteObject(ctx, objectKey); err != nil {
		log.Errorf("failed to delete S3 Object for problem %s: %v", problemID, err)
	}

	return nil
}

//Leaderboard related services

func (cs *ContestService) UpdateLeaderboardUser(ctx context.Context, contestID string, userID string, req *dto.UpdateLeaderboardUserRequest) error {
	return cs.stores.Rankings.UpdateLeaderboardUser(ctx, contestID, userID, req)
}

func (cs *ContestService) GetLeaderboard(ctx context.Context, contestID string, page int) (*dto.GetLeaderboardResponse, error) {
	contest, err := cs.stores.Contests.GetContest(ctx, contestID)
	if err != nil {
		return nil, err
	}
	if contest.GetRunningStatus() == models.ContestRunningUpcoming {
		return nil, common.ErrContestNotStarted
	}
	return cs.stores.Rankings.GetLeaderboard(ctx, contestID, page)
}

func (cs *ContestService) GetProblemVisibility(ctx context.Context, contestID string, userID string) error {

	contest, err := cs.GetContest(ctx, contestID, userID)
	if err != nil {
		return err
	}

	if contest.GetRunningStatus() == models.ContestRunningUpcoming {
		return common.ContestNotRunningError
	}

	// While the contest is running/open, only registered users can view the problems.
	// Once the contest is closed/over, anyone (including unauthenticated users) can view
	// the problems – useful for post-contest review.
	if contest.GetRunningStatus() == models.ContestRunningOpen {
		if contest.IsRegistered == nil || !*contest.IsRegistered {
			return common.UserNotRegisteredError
		}
	}

	return nil
}

func (cs *ContestService) GetContestProblemsList(ctx context.Context, contestID string) ([]dto.ProblemOverview, error) {
	return cs.stores.Problems.GetProblemList(ctx, contestID)
}

func (cs *ContestService) GetContestProblemsListAdmin(ctx context.Context, contestID string) ([]dto.ProblemOverview, error) {
	return cs.stores.Problems.GetProblemList(ctx, contestID)
}

func (cs *ContestService) GetContestProblem(ctx context.Context, contestID string, problemID string, includeAdminFields bool) (*dto.GetProblemStatementResponse, error) {

	meta, err := cs.stores.Problems.GetProblem(ctx, problemID, contestID)
	if err != nil {
		return nil, err
	}

	s3Key := meta.Description

	if strings.HasPrefix(s3Key, "problems/") {
		desc, err := cs.s3.GetObject(ctx, s3Key)
		if err != nil {
			return nil, err
		}

		var data struct {
			Description string `json:"description"`
		}

		if err := json.Unmarshal([]byte(desc), &data); err != nil {
			return nil, fmt.Errorf("unmarshal problem description: %w", err)
		}

		meta.Description = data.Description
	}
	if meta.Type == models.Code && meta.TestcasesKey != "" {
		// ponytail: testcases are secondary data. A missing or corrupt object must not
		// take the problem statement down with it -- log and serve the statement.
		if testcase, err := cs.s3.GetObject(ctx, meta.TestcasesKey); err != nil {
			log.Errorf("failed to load testcases for problem %s: %v", problemID, err)
		} else {
			var tcArr []dto.TestCaseResponse
			if err := json.Unmarshal([]byte(testcase), &tcArr); err != nil {
				log.Errorf("failed to parse testcases for problem %s: %v", problemID, err)
			} else {
				if includeAdminFields {
					answers, err := cs.readProblemAnswers(ctx, meta.TestcasesKey)
					if err != nil {
						return nil, err
					}
					if len(answers) != len(tcArr) {
						return nil, common.ErrNoTestcases
					}
					for i, tc := range tcArr {
						if tc.Index < 0 || tc.Index >= len(answers) {
							return nil, common.ErrNoTestcases
						}
						tcArr[i].ExpectedOutput = answers[tc.Index]
					}
					meta.Testcases = tcArr
				} else {
					sampleCases := []dto.TestCaseResponse{}
					for _, tc := range tcArr {
						if tc.IsSample {
							sampleCases = append(sampleCases, tc)
						}
					}
					// A sample is only useful if the contestant can see the expected
					// output, which lives in answers.json. Hidden cases keep theirs
					// server-side.
					if len(sampleCases) > 0 {
						if answers, err := cs.readProblemAnswers(ctx, meta.TestcasesKey); err != nil {
							log.Errorf("failed to load answers for problem %s: %v", problemID, err)
						} else {
							for i, tc := range sampleCases {
								if tc.Index >= 0 && tc.Index < len(answers) {
									sampleCases[i].ExpectedOutput = answers[tc.Index]
								}
							}
						}
					}
					meta.Testcases = sampleCases
				}
			}
		}
	}
	if !includeAdminFields {
		meta.Answer = nil
	}

	return meta, nil
}

func (cs *ContestService) GetContest(ctx context.Context, contestID string, userID string) (*dto.GetContestResponse, error) {
	contest_response, err := cs.stores.Contests.GetContest(ctx, contestID)
	if err != nil {
		return nil, err
	}

	if userID == "" {
		return contest_response, nil
	}

	r, err := cs.stores.Contests.IsRegistered(ctx, contestID, userID)
	if err != nil {
		return nil, err
	}

	contest_response.IsRegistered = &r
	return contest_response, nil
}

func (cs *ContestService) GetContestRegistrations(ctx context.Context, contestID string) ([]dto.ContestRegistration, error) {
	return cs.stores.Contests.GetContestRegistrations(ctx, contestID)
}

func (cs *ContestService) GetProblemTestcases(ctx context.Context, contestID, problemID string) ([]dto.TestCaseResponse, error) {

	meta, err := cs.stores.Problems.GetProblem(ctx, problemID, contestID)
	if err != nil {
		return nil, err
	}

	if meta.Type != models.Code || meta.TestcasesKey == "" {
		return []dto.TestCaseResponse{}, nil
	}

	raw, err := cs.s3.GetObject(ctx, meta.TestcasesKey)
	if err != nil {
		return nil, err
	}

	var arr []dto.TestCaseResponse
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil, err
	}

	return arr, nil
}

func (cs *ContestService) GetProblemAnswers(ctx context.Context, contestID, problemID string) ([]string, error) {

	meta, err := cs.stores.Problems.GetProblem(ctx, problemID, contestID)
	if err != nil {
		return nil, err
	}

	if meta.Type != models.Code || meta.TestcasesKey == "" {
		return []string{}, nil
	}

	return cs.readProblemAnswers(ctx, meta.TestcasesKey)
}

func (cs *ContestService) readProblemAnswers(ctx context.Context, testcasesKey string) ([]string, error) {
	raw, err := cs.s3.GetObject(ctx, testcaseAnswersKey(testcasesKey))
	if err != nil {
		return nil, err
	}

	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil, err
	}

	return arr, nil
}

func preserveProblemLimits(problem *models.Problem, meta *dto.GetProblemStatementResponse) {
	if problem.TimeLimit <= 0 {
		problem.TimeLimit = meta.TimeLimit
	}
	if problem.MemoryLimit <= 0 {
		problem.MemoryLimit = meta.MemoryLimit
	}
}

func validateMCQAnswerRange(req *dto.CreateProblemRequest) error {
	if req.Type == models.MCQ {
		for _, answer := range req.Answer {
			if answer < 0 || answer >= len(req.Options) {
				return common.ErrInvalidAnswer
			}
		}
	}
	return nil
}

func (cs *ContestService) checkProblemEditable(ctx context.Context, contestID string) error {
	// shortcut: finish in-flight edits before opening; use transactional authoring if edits must overlap the start.
	contest, err := cs.stores.Contests.GetContest(ctx, contestID)
	if err != nil {
		return err
	}
	if contest.GetRunningStatus() != models.ContestRunningUpcoming {
		return common.ErrProblemsLocked
	}
	return nil
}

func (cs *ContestService) ConsolidateContest(ctx context.Context, contestID string, regradeMCQs bool, force bool) (*dto.ConsolidationResponse, error) {
	if cs == nil || cs.stores == nil || cs.stores.Rankings == nil {
		return nil, fmt.Errorf("contest service: ranking store not initialized")
	}

	if !force && cs.stores.Contests != nil {
		contest, err := cs.stores.Contests.GetContest(ctx, contestID)
		if err != nil {
			return nil, err
		}
		if contest.GetRunningStatus() != models.ContestRunningClosed {
			return nil, common.ErrContestNotClosed
		}
	}

	return cs.stores.Rankings.ConsolidateContest(ctx, contestID, regradeMCQs, force)
}
