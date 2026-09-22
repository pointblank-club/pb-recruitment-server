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

	s3Key := fmt.Sprintf("problems/%s/%s.json", problem.ContestID, problem.ID)

	payload := map[string]string{
		"description": req.Description,
	}

	data, _ := json.Marshal(payload)

	if err := cs.s3.PutObject(ctx, s3Key, string(data)); err != nil {
		return nil, err
	}

	problem.Description = s3Key

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

	// Use existing S3 key if it's valid, otherwise create a new one
	s3Key := meta.Description
	if s3Key == "" || !strings.HasPrefix(s3Key, "problems/") {
		// Create new S3 key for NULL/empty descriptions or legacy direct descriptions
		s3Key = fmt.Sprintf("problems/%s/%s.json", contestID, problemID)
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
	}

	if err := cs.stores.Problems.UpdateProblem(ctx, problem); err != nil {
		return nil, err
	}
	return problem, nil
}

func (cs *ContestService) DeleteProblem(ctx context.Context, contestID string, problemID string) error {

	meta, err := cs.stores.Problems.GetProblem(ctx, problemID, contestID)
	if err != nil {
		return err
	}

	s3Key := meta.Description

	if err := cs.stores.Problems.DeleteProblem(ctx, contestID, problemID); err != nil {
		return err
	}

	if strings.HasPrefix(s3Key, "problems/") {
		if err := cs.s3.DeleteObject(ctx, s3Key); err != nil {
			log.Errorf("failed to delete S3 file for problem %s: %v", problemID, err)
		}
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

func (cs *ContestService) GetContestProblemsListAdmin(ctx context.Context, contestID string) ([]dto.ProblemOverview, error) {
	return cs.stores.Problems.GetProblemList(ctx, contestID)
}

func (cs *ContestService) GetContestProblem(ctx context.Context, contestID string, problemID string) (*dto.GetProblemStatementResponse, error) {

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
		meta.Description = desc
	}

	return meta, nil
}

func (cs *ContestService) GetContestProblemAdmin(ctx context.Context, contestID string, problemID string) (*dto.GetProblemStatementResponse, error) {
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
		meta.Description = desc
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
