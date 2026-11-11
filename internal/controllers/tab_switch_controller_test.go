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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

type tabSwitchMockStore struct {
	submissionContestStore
	currentCount     int
	generation       int
	isTerminated     bool
	history          []map[string]any
	seen             map[int]map[string]struct{}
	unregisterCalled bool
}

func (s *tabSwitchMockStore) RecordTabSwitch(_ context.Context, _ string, _ string, observationID string, generation int, count int) (*dto.TabSwitchResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if !s.registered {
		return nil, common.UserNotRegisteredError
	}
	if s.contest.GetRunningStatus() != models.ContestRunningOpen {
		return nil, common.ContestNotRunningError
	}
	if generation != s.generation {
		return nil, &common.TabSwitchStaleGenerationError{
			TabSwitchCount: s.currentCount,
			IsTerminated:   s.isTerminated,
			Generation:     s.generation,
		}
	}
	if s.isTerminated {
		return &dto.TabSwitchResponse{
			TabSwitchCount: s.currentCount,
			IsTerminated:   true,
			Generation:     s.generation,
		}, nil
	}
	if s.seen == nil {
		s.seen = make(map[int]map[string]struct{})
	}
	if s.seen[generation] == nil {
		s.seen[generation] = make(map[string]struct{})
	}
	if _, dup := s.seen[generation][observationID]; dup {
		return &dto.TabSwitchResponse{
			TabSwitchCount: s.currentCount,
			IsTerminated:   s.isTerminated,
			Generation:     s.generation,
		}, nil
	}
	s.seen[generation][observationID] = struct{}{}

	s.currentCount += count
	if s.currentCount > dto.TabSwitchTerminationThreshold {
		s.currentCount = dto.TabSwitchTerminationThreshold
	}
	if s.currentCount >= dto.TabSwitchTerminationThreshold {
		s.isTerminated = true
	}
	s.history = append(s.history, map[string]any{
		"timestamp":      time.Now().UnixMilli(),
		"count":          s.currentCount,
		"delta":          count,
		"observation_id": observationID,
		"generation":     s.generation,
	})
	return &dto.TabSwitchResponse{
		TabSwitchCount: s.currentCount,
		IsTerminated:   s.isTerminated,
		Generation:     s.generation,
	}, nil
}

func (s *tabSwitchMockStore) GetCandidateSessionStatus(_ context.Context, _ string, _ string) (*dto.CandidateSessionStatusResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if !s.registered {
		return nil, common.UserNotRegisteredError
	}
	return &dto.CandidateSessionStatusResponse{
		TabSwitchCount:        s.currentCount,
		IsTabSwitchTerminated: s.isTerminated,
		Generation:            s.generation,
	}, nil
}

func (s *tabSwitchMockStore) ResetContestantTabSwitches(_ context.Context, _ string, _ string, adminID string) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if !s.registered {
		return 0, common.UserNotRegisteredError
	}
	s.currentCount = 0
	s.isTerminated = false
	s.generation++
	s.history = append(s.history, map[string]any{
		"action":     "admin_reset",
		"admin_id":   adminID,
		"generation": s.generation,
		"timestamp":  time.Now().UnixMilli(),
	})
	return s.generation, nil
}

func (s *tabSwitchMockStore) UnregisterUser(_ context.Context, _ string, _ string) error {
	s.unregisterCalled = true
	s.registered = false
	return nil
}

func intPtr(v int) *int { return &v }

func testUUID(i int) string {
	return fmt.Sprintf("11111111-1111-4111-8111-%012d", i)
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

	send := func(obsID string, gen, count int) *httptest.ResponseRecorder {
		e := echo.New()
		reqBody, _ := json.Marshal(dto.TabSwitchRequest{
			ObservationID: obsID,
			Generation:    intPtr(gen),
			Count:         intPtr(count),
		})
		req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ctx := e.NewContext(req, rec)
		ctx.SetParamNames("contestId")
		ctx.SetParamValues("c1")
		ctx.Set(common.AUTH_USER_ID, "candidate-1")
		if err := controller.RecordTabSwitch(ctx); err != nil {
			t.Fatalf("RecordTabSwitch failed: %v", err)
		}
		return rec
	}

	// Switches 1 through 4: unique observations, not terminated
	for i := 1; i <= 4; i++ {
		rec := send(testUUID(i), 0, 1)
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

	// Identical retry of switch 4 returns 4 without incrementing to 5
	retryRec := send(testUUID(4), 0, 1)
	if retryRec.Code != http.StatusOK {
		t.Fatalf("retry got HTTP %d, want 200", retryRec.Code)
	}
	var retryRes dto.TabSwitchResponse
	_ = json.Unmarshal(retryRec.Body.Bytes(), &retryRes)
	if retryRes.TabSwitchCount != 4 || retryRes.IsTerminated {
		t.Fatalf("retry mutated state: %+v", retryRes)
	}

	// Switch 5: triggers termination
	rec5 := send(testUUID(5), 0, 1)
	if rec5.Code != http.StatusOK {
		t.Fatalf("switch 5 got HTTP %d: %s", rec5.Code, rec5.Body.String())
	}
	var res5 dto.TabSwitchResponse
	_ = json.Unmarshal(rec5.Body.Bytes(), &res5)
	if res5.TabSwitchCount != 5 || !res5.IsTerminated {
		t.Fatalf("switch 5 unexpected result: %+v", res5)
	}

	// Switch 6: remains terminated, count capped at 5, history does not grow
	rec6 := send(testUUID(6), 0, 1)
	var res6 dto.TabSwitchResponse
	_ = json.Unmarshal(rec6.Body.Bytes(), &res6)
	if !res6.IsTerminated || res6.TabSwitchCount != 5 {
		t.Fatalf("switch 6 expected count=5 terminated=true, got %+v", res6)
	}
	if len(mockStore.history) != 5 {
		t.Fatalf("expected history length 5, got %d", len(mockStore.history))
	}
}

func TestRecordTabSwitchValidationAndConflicts(t *testing.T) {
	now := time.Now().UnixMilli()
	mockStore := &tabSwitchMockStore{
		submissionContestStore: submissionContestStore{
			contest:    models.Contest{ID: "c1", StartTime: now - 60_000, EndTime: now + 60_000},
			registered: true,
		},
		currentCount: 2,
		generation:   1,
	}
	controller := NewContestController(services.NewContestService(&stores.Storage{Contests: mockStore}, nil), nil)

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"empty body", `{}`, http.StatusBadRequest},
		{"zero count", `{"observation_id":"` + testUUID(1) + `","generation":1,"count":0}`, http.StatusBadRequest},
		{"negative count", `{"observation_id":"` + testUUID(1) + `","generation":1,"count":-1}`, http.StatusBadRequest},
		{"count = 2", `{"observation_id":"` + testUUID(1) + `","generation":1,"count":2}`, http.StatusBadRequest},
		{"count = 5", `{"observation_id":"` + testUUID(1) + `","generation":1,"count":5}`, http.StatusBadRequest},
		{"count > 5", `{"observation_id":"` + testUUID(1) + `","generation":1,"count":6}`, http.StatusBadRequest},
		{"int32 overflow", `{"observation_id":"` + testUUID(1) + `","generation":1,"count":2147483648}`, http.StatusBadRequest},
		{"stale generation", `{"observation_id":"` + testUUID(1) + `","generation":0,"count":1}`, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			ctx := e.NewContext(req, rec)
			ctx.SetParamNames("contestId")
			ctx.SetParamValues("c1")
			ctx.Set(common.AUTH_USER_ID, "candidate-1")

			if err := controller.RecordTabSwitch(ctx); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.want {
				t.Fatalf("got HTTP %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusConflict {
				var conflict dto.TabSwitchConflictResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
					t.Fatal(err)
				}
				if conflict.Generation != 1 || conflict.TabSwitchCount != 2 || conflict.IsTerminated {
					t.Fatalf("unexpected conflict body: %+v", conflict)
				}
			}
		})
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
			req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", bytes.NewBufferString(`{"observation_id":"`+testUUID(1)+`","generation":0,"count":1}`))
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
	req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", bytes.NewBufferString(`{"observation_id":"`+testUUID(1)+`","generation":0,"count":1}`))
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
		generation:   2,
		isTerminated: false,
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
	if status.TabSwitchCount != 3 || status.IsTabSwitchTerminated || status.Generation != 2 {
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
	if !res.Success || res.Message == "" || res.Generation != 1 {
		t.Fatalf("unexpected reset response: %+v", res)
	}

	if mockStore.currentCount != 0 || mockStore.isTerminated || mockStore.generation != 1 {
		t.Fatalf("store was not reset: count=%d, terminated=%v, gen=%d",
			mockStore.currentCount, mockStore.isTerminated, mockStore.generation)
	}
}

func TestModifyRegistrationRejectsUnregisterAfterStart(t *testing.T) {
	now := time.Now().UnixMilli()
	mockStore := &tabSwitchMockStore{
		submissionContestStore: submissionContestStore{
			contest: models.Contest{
				ID:                    "c1",
				RegistrationStartTime: now - 120_000,
				RegistrationEndTime:   now + 60_000,
				StartTime:             now - 60_000,
				EndTime:               now + 60_000,
			},
			registered: true,
		},
	}
	controller := NewContestController(services.NewContestService(&stores.Storage{Contests: mockStore}, nil), nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/contests/c1/registration", nil)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	ctx.SetParamNames("id")
	ctx.SetParamValues("c1")
	ctx.Set(common.AUTH_USER_ID, "candidate-1")
	ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.ModifyRegistrationRequest{Action: dto.UnregisterAction})

	if err := controller.ModifyRegistration(ctx); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d: %s", rec.Code, rec.Body.String())
	}
	if mockStore.unregisterCalled {
		t.Fatal("UnregisterUser should not have been called after contest start")
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
			Submissions: &controllerSubmissionStore{contests: mockContests},
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

	t.Run("active candidate permitted to submit", func(t *testing.T) {
		mockContests := &tabSwitchMockStore{
			submissionContestStore: submissionContestStore{
				contest:    models.Contest{StartTime: now - 60_000, EndTime: now + 60_000},
				registered: true,
			},
			isTerminated: false,
		}
		subStore := &controllerSubmissionStore{contests: mockContests}
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
