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

	problem := &models.Problem{
		ID:                 uuid.NewString(),
		ContestID:          contestID,
		Name:               req.Name,
		Score:              req.Score,
		Type:               req.Type,
		Answer:             req.Answer,
		HasMultipleAnswers: req.Type == "mcq" && len(req.Answer) > 1,
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
		answersKey := fmt.Sprintf("problems/%s/%s/answers.json", contestID, problem.ID)

		var tcArray []map[string]interface{}
		var ansArray []string

		for i, tc := range req.Testcases {
			tcArray = append(tcArray, map[string]interface{}{
				"index": i,
				"input": tc.Input,
			})

			ansArray = append(ansArray, tc.ExpectedOutput)
		}

		tcBytes, _ := json.Marshal(tcArray)
		ansBytes, _ := json.Marshal(ansArray)

		if err := cs.s3.PutObject(ctx, testcasesKey, string(tcBytes)); err != nil {
			return nil, err
		}

		if err := cs.s3.PutObject(ctx, answersKey, string(ansBytes)); err != nil {
			return nil, err
		}

		problem.Testcases = testcasesKey
	}

	if err := cs.stores.Problems.CreateProblem(ctx, problem); err != nil {
		return nil, err
	}

	return problem, nil
}

func (cs *ContestService) UpdateProblem(ctx context.Context, contestID string, problemID string, req *dto.CreateProblemRequest) (*models.Problem, error) {

	meta, err := cs.stores.Problems.GetProblem(ctx, problemID, contestID)
	if err != nil {
		return nil, err
	}

	s3Key := meta.Description

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

	var testcasesKey string = meta.TestcasesKey

	if req.Type == "code" && len(req.Testcases) > 0 {

		testcasesKey = fmt.Sprintf("problems/%s/%s/testcases.json", contestID, problemID)
		answersKey := fmt.Sprintf("problems/%s/%s/answers.json", contestID, problemID)

		var tcArray []map[string]interface{}
		var ansArray []string

		for i, tc := range req.Testcases {
			tcArray = append(tcArray, map[string]interface{}{
				"index": i,
				"input": tc.Input,
			})

			ansArray = append(ansArray, tc.ExpectedOutput)
		}

		tcBytes, _ := json.Marshal(tcArray)
		if err := cs.s3.PutObjectOverwrite(ctx, testcasesKey, string(tcBytes)); err != nil {
			return nil, err
		}

		ansBytes, _ := json.Marshal(ansArray)
		if err := cs.s3.PutObjectOverwrite(ctx, answersKey, string(ansBytes)); err != nil {
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
		HasMultipleAnswers: hasMultiple,
		Testcases:          testcasesKey,
	}

	if err := cs.stores.Problems.UpdateProblem(ctx, problem); err != nil {
		return nil, err
	}
	return problem, nil
}

func (cs *ContestService) DeleteProblem(ctx context.Context, contestID string, problemID string) error {

	_, err := cs.stores.Problems.GetProblem(ctx, problemID, contestID)
	if err != nil {
		return err
	}

	if err := cs.stores.Problems.DeleteProblem(ctx, contestID, problemID); err != nil {
		return err
	}

	prefix := fmt.Sprintf("problems/%s/%s/", contestID, problemID)

	if err := cs.s3.DeleteObject(ctx, prefix); err != nil {
		log.Errorf("failed to delete S3 folder for problem %s: %v", problemID, err)
	}

	return nil
}

//Leaderboard related services

func (cs *ContestService) UpdateLeaderboardUser(ctx context.Context, contestID string, userID string, req *dto.UpdateLeaderboardUserRequest) error {
	return cs.stores.Rankings.UpdateLeaderboardUser(ctx, contestID, userID, req)
}

func (cs *ContestService) GetProblemVisibility(ctx context.Context, contestID string, userID string) error {

	contest, err := cs.GetContest(ctx, contestID, userID)
	if err != nil {
		return err
	}

	if contest.IsRegistered == nil || !*contest.IsRegistered {
		return common.UserNotRegisteredError
	}

	if contest.GetRunningStatus() == models.ContestRunningUpcoming {
		return common.ContestNotRunningError
	}

	return nil
}

func (cs *ContestService) GetContestProblemsList(ctx context.Context, contestID string) ([]dto.ProblemOverview, error) {
	return cs.stores.Problems.GetProblemList(ctx, contestID)
}

func (cs *ContestService) GetContestProblem(ctx context.Context, contestID string, problemID string) (*dto.GetProblemStatementResponse, error) {

	meta, err := cs.stores.Problems.GetProblem(ctx, problemID, contestID)
	if err != nil {
		return nil, err
	}

	descKey := meta.Description

	desc, err := cs.s3.GetObject(ctx, descKey)
	if err != nil {
		return nil, err
	}

	meta.Description = desc

	testcaseKey := meta.TestcasesKey

	testcase, err := cs.s3.GetObject(ctx, testcaseKey)
	if err != nil {
		return meta, nil
	}

	var tcArr []dto.TestCaseResponse
	if err := json.Unmarshal([]byte(testcase), &tcArr); err != nil {
		return nil, err
	}

	meta.Testcases = tcArr

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

	if meta.TestcasesKey == "" {
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

	if meta.TestcasesKey == "" {
		return []string{}, nil
	}

	answersKey := strings.Replace(meta.TestcasesKey, "testcases.json", "answers.json", 1)

	raw, err := cs.s3.GetObject(ctx, answersKey)
	if err != nil {
		return nil, err
	}

	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil, err
	}

	return arr, nil
}
