package services

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"context"
	"regexp"
)

var sebUserAgent = regexp.MustCompile(`(?i)(?:^|[\s;(])SEB/[0-9]+(?:\.[0-9]+)*(?:$|[\s;)])`)

func (cs *ContestService) BrowserSession(ctx context.Context, contestID, userID, token, attemptID, userAgent string, create bool, generation *int) (*dto.BrowserSessionResponse, string, error) {
	if cs == nil || cs.stores == nil || cs.stores.Contests == nil {
		return nil, "", common.ErrBrowserSessionUnavailable
	}
	contest, err := cs.stores.Contests.GetContest(ctx, contestID)
	if err != nil {
		return nil, "", err
	}
	if contest.GetRunningStatus() != models.ContestRunningOpen {
		if create {
			return nil, "", common.ContestNotRunningError
		}
		return &dto.BrowserSessionResponse{}, "", nil
	}
	if cs.stores.SEB == nil {
		return nil, "", common.ErrBrowserSessionUnavailable
	}
	return cs.stores.SEB.Observe(ctx, contestID, userID, token, attemptID, sebUserAgent.MatchString(userAgent), create, generation)
}

func (cs *ContestService) ResetBrowserSessions(ctx context.Context, contestID, userID, adminID string) (int, error) {
	return cs.stores.SEB.Reset(ctx, contestID, userID, adminID)
}
