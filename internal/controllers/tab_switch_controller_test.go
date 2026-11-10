package controllers

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

type tabSwitchMockStore struct {
	submissionContestStore
	currentCount int
	isTerminated bool
	isSubmitted  bool
	history      []map[string]any
}

func (s *tabSwitchMockStore) RecordTabSwitch(_ context.Context, contestID string, userID string, count int, ip string, userAgent string) (*dto.TabSwitchResponse, error) {
	wasTerminated := s.isTerminated
	if count > 0 && count > s.currentCount {
		s.currentCount = count
	} else {
		s.currentCount++
	}
	if s.currentCount >= 5 {
		s.isTerminated = true
	}
	if !wasTerminated {
		s.history = append(s.history, map[string]any{
			"timestamp":  time.Now().UnixMilli(),
			"count":      s.currentCount,
			"ip":         ip,
			"user_agent": userAgent,
		})
	}
	return &dto.TabSwitchResponse{
		TabSwitchCount: s.currentCount,
		IsTerminated:   s.isTerminated,
	}, nil
}

func (s *tabSwitchMockStore) GetCandidateSessionStatus(_ context.Context, contestID string, userID string) (*dto.CandidateSessionStatusResponse, error) {
	return &dto.CandidateSessionStatusResponse{
		TabSwitchCount:        s.currentCount,
		IsTabSwitchTerminated: s.isTerminated,
		IsSubmitted:           s.isSubmitted,
	}, nil
}

func (s *tabSwitchMockStore) ResetContestantTabSwitches(_ context.Context, contestID string, userID string, adminID string) error {
	s.currentCount = 0
	s.isTerminated = false
	s.isSubmitted = false
	s.history = append(s.history, map[string]any{
		"action":    "admin_reset",
		"admin_id":  adminID,
		"timestamp": time.Now().UnixMilli(),
	})
	return nil
}

func TestRecordTabSwitchProgression(t *testing.T) {
	now := time.Now().UnixMilli()
	mockStore := &tabSwitchMockStore{
		submissionContestStore: submissionContestStore{
			contest:    models.Contest{ID: "c1", StartTime: now - 60_000, EndTime: now + 60_000},
			registered: true,
		},
	}
	storage := &stores.Storage{Contests: mockStore}
	controller := NewContestController(services.NewContestService(storage, nil), nil)

	// Switches 1 through 4: not terminated
	for i := 1; i <= 4; i++ {
		e := echo.New()
		reqBody, _ := json.Marshal(dto.TabSwitchRequest{Count: i})
		req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ctx := e.NewContext(req, rec)
		ctx.SetParamNames("contestId")
		ctx.SetParamValues("c1")
		ctx.Set(common.AUTH_USER_ID, "candidate-1")

		if err := controller.RecordTabSwitch(ctx); err != nil {
			t.Fatalf("switch %d failed: %v", i, err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("switch %d got HTTP %d, want 200: %s", i, rec.Code, rec.Body.String())
		}

		var res dto.TabSwitchResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("switch %d decode response: %v", i, err)
		}
		if res.TabSwitchCount != i {
			t.Fatalf("switch %d got count %d, want %d", i, res.TabSwitchCount, i)
		}
		if res.IsTerminated {
			t.Fatalf("switch %d expected is_terminated false, got true", i)
		}
	}

	// Switch 5: triggers termination
	{
		e := echo.New()
		reqBody, _ := json.Marshal(dto.TabSwitchRequest{Count: 5})
		req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ctx := e.NewContext(req, rec)
		ctx.SetParamNames("contestId")
		ctx.SetParamValues("c1")
		ctx.Set(common.AUTH_USER_ID, "candidate-1")

		if err := controller.RecordTabSwitch(ctx); err != nil {
			t.Fatalf("switch 5 failed: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("switch 5 got HTTP %d: %s", rec.Code, rec.Body.String())
		}

		var res dto.TabSwitchResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("switch 5 decode response: %v", err)
		}
		if res.TabSwitchCount != 5 {
			t.Fatalf("switch 5 got count %d, want 5", res.TabSwitchCount)
		}
		if !res.IsTerminated {
			t.Fatalf("switch 5 expected is_terminated true, got false")
		}
	}

	// Switch 6: remains terminated
	{
		e := echo.New()
		reqBody, _ := json.Marshal(dto.TabSwitchRequest{Count: 6})
		req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ctx := e.NewContext(req, rec)
		ctx.SetParamNames("contestId")
		ctx.SetParamValues("c1")
		ctx.Set(common.AUTH_USER_ID, "candidate-1")

		if err := controller.RecordTabSwitch(ctx); err != nil {
			t.Fatalf("switch 6 failed: %v", err)
		}
		var res dto.TabSwitchResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		if !res.IsTerminated {
			t.Fatalf("switch 6 expected is_terminated true")
		}
		if res.TabSwitchCount != 6 {
			t.Fatalf("switch 6 expected count 6, got %d", res.TabSwitchCount)
		}
		if len(mockStore.history) != 5 {
			t.Fatalf("expected history length 5 (no history appended after termination), got %d", len(mockStore.history))
		}
	}
}

func TestRecordTabSwitchContestNotRunning(t *testing.T) {
	now := time.Now().UnixMilli()

	for _, tc := range []struct {
		name  string
		start int64
		end   int64
	}{
		{"upcoming contest", now + 60_000, now + 120_000},
		{"closed contest", now - 120_000, now - 60_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &stores.Storage{
				Contests: &tabSwitchMockStore{
					submissionContestStore: submissionContestStore{
						contest:    models.Contest{ID: "c1", StartTime: tc.start, EndTime: tc.end},
						registered: true,
					},
				},
			}
			controller := NewContestController(services.NewContestService(storage, nil), nil)
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", bytes.NewReader([]byte(`{"count": 1}`)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			ctx := e.NewContext(req, rec)
			ctx.SetParamNames("contestId")
			ctx.SetParamValues("c1")
			ctx.Set(common.AUTH_USER_ID, "candidate-1")

			if err := controller.RecordTabSwitch(ctx); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected status 403, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRecordTabSwitchNotRegistered(t *testing.T) {
	now := time.Now().UnixMilli()
	storage := &stores.Storage{
		Contests: &tabSwitchMockStore{
			submissionContestStore: submissionContestStore{
				contest:    models.Contest{ID: "c1", StartTime: now - 60_000, EndTime: now + 60_000},
				registered: false,
			},
		},
	}
	controller := NewContestController(services.NewContestService(storage, nil), nil)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", bytes.NewReader([]byte(`{"count": 1}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	ctx.SetParamNames("contestId")
	ctx.SetParamValues("c1")
	ctx.Set(common.AUTH_USER_ID, "candidate-1")

	if err := controller.RecordTabSwitch(ctx); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetCandidateSessionStatus(t *testing.T) {
	now := time.Now().UnixMilli()
	mockStore := &tabSwitchMockStore{
		submissionContestStore: submissionContestStore{
			contest:    models.Contest{ID: "c1", StartTime: now - 60_000, EndTime: now + 60_000},
			registered: true,
		},
		currentCount: 3,
		isTerminated: false,
		isSubmitted:  false,
	}
	storage := &stores.Storage{Contests: mockStore}
	controller := NewContestController(services.NewContestService(storage, nil), nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/contests/c1/session-status", nil)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	ctx.SetParamNames("contestId")
	ctx.SetParamValues("c1")
	ctx.Set(common.AUTH_USER_ID, "candidate-1")

	if err := controller.GetCandidateSessionStatus(ctx); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var status dto.CandidateSessionStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.TabSwitchCount != 3 || status.IsTabSwitchTerminated || status.IsSubmitted {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestResetContestantTabSwitches(t *testing.T) {
	now := time.Now().UnixMilli()
	mockStore := &tabSwitchMockStore{
		submissionContestStore: submissionContestStore{
			contest:    models.Contest{ID: "c1", StartTime: now - 60_000, EndTime: now + 60_000},
			registered: true,
		},
		currentCount: 5,
		isTerminated: true,
		isSubmitted:  true,
	}
	storage := &stores.Storage{Contests: mockStore}
	controller := NewContestController(services.NewContestService(storage, nil), nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/contests/c1/contestants/user-1/reset-tab-switches", nil)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	ctx.SetParamNames("contestId", "userId")
	ctx.SetParamValues("c1", "user-1")
	ctx.Set(common.AUTH_USER_ID, "admin-1")

	if err := controller.ResetContestantTabSwitches(ctx); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res dto.ResetTabSwitchesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Success || res.Message == "" {
		t.Fatalf("unexpected reset response: %+v", res)
	}

	if mockStore.currentCount != 0 || mockStore.isTerminated || mockStore.isSubmitted {
		t.Fatalf("store was not reset: count=%d, terminated=%v, submitted=%v",
			mockStore.currentCount, mockStore.isTerminated, mockStore.isSubmitted)
	}
}

func TestSubmitSolutionTabSwitchLockout(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("terminated candidate locked out", func(t *testing.T) {
		mockContests := &tabSwitchMockStore{
			submissionContestStore: submissionContestStore{
				contest:    models.Contest{StartTime: now - 60_000, EndTime: now + 60_000},
				registered: true,
			},
			isTerminated: true,
		}
		storage := &stores.Storage{
			Contests:    mockContests,
			Problems:    submissionProblemStore{},
			Submissions: &controllerSubmissionStore{},
		}
		controller := NewSubmissionController(services.NewSubmissionService(storage, nil, nil), services.NewContestService(storage, nil), nil)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/submit", nil), recorder)
		ctx.Set(common.AUTH_USER_ID, "user")
		ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: models.MCQ, Option: []int{1}})

		if err := controller.SubmitSolution(ctx); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("HTTP %d, want 403 Forbidden", recorder.Code)
		}
		if !bytes.Contains(recorder.Body.Bytes(), []byte("contest participation terminated due to tab switching")) {
			t.Fatalf("unexpected response body: %s", recorder.Body.String())
		}
	})

	t.Run("submitted candidate locked out", func(t *testing.T) {
		mockContests := &tabSwitchMockStore{
			submissionContestStore: submissionContestStore{
				contest:    models.Contest{StartTime: now - 60_000, EndTime: now + 60_000},
				registered: true,
			},
			isSubmitted: true,
		}
		storage := &stores.Storage{
			Contests:    mockContests,
			Problems:    submissionProblemStore{},
			Submissions: &controllerSubmissionStore{},
		}
		controller := NewSubmissionController(services.NewSubmissionService(storage, nil, nil), services.NewContestService(storage, nil), nil)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/submit", nil), recorder)
		ctx.Set(common.AUTH_USER_ID, "user")
		ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: models.MCQ, Option: []int{1}})

		if err := controller.SubmitSolution(ctx); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("HTTP %d, want 403 Forbidden", recorder.Code)
		}
		if !bytes.Contains(recorder.Body.Bytes(), []byte("contest already submitted")) {
			t.Fatalf("unexpected response body: %s", recorder.Body.String())
		}
	})

	t.Run("active candidate permitted to submit", func(t *testing.T) {
		subStore := &controllerSubmissionStore{}
		mockContests := &tabSwitchMockStore{
			submissionContestStore: submissionContestStore{
				contest:    models.Contest{StartTime: now - 60_000, EndTime: now + 60_000},
				registered: true,
			},
			isTerminated: false,
			isSubmitted:  false,
		}
		storage := &stores.Storage{
			Contests:    mockContests,
			Problems:    submissionProblemStore{},
			Submissions: subStore,
		}
		controller := NewSubmissionController(services.NewSubmissionService(storage, nil, nil), services.NewContestService(storage, nil), nil)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/submit", nil), recorder)
		ctx.Set(common.AUTH_USER_ID, "user")
		ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: models.MCQ, Option: []int{1}})

		if err := controller.SubmitSolution(ctx); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusCreated {
			t.Fatalf("HTTP %d, want 201 Created: %s", recorder.Code, recorder.Body.String())
		}
		if !subStore.created {
			t.Fatalf("expected submission to be created")
		}
	})
}
