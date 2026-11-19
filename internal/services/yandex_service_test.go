package services

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/stores"
	"app/internal/yandex"
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

var yandexTestKey = []byte("0123456789abcdef0123456789abcdef")

type yandexTestStore struct {
	accounts map[string]*models.YandexAccount
	saveErr  error
	savedAt  int64
}

func (s *yandexTestStore) GetYandexAccount(_ context.Context, userID string) (*models.YandexAccount, error) {
	if a, ok := s.accounts[userID]; ok {
		return a, nil
	}
	return nil, common.ErrNotFound
}

func (s *yandexTestStore) HasYandexAccount(_ context.Context, userID string) (bool, error) {
	_, ok := s.accounts[userID]
	return ok, nil
}

func (s *yandexTestStore) SaveYandexAccount(_ context.Context, a *models.YandexAccount, now int64) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.accounts[a.UserID] = a
	s.savedAt = now
	return nil
}

type yandexTestUsers struct{ *stores.UserStore }

func (yandexTestUsers) GetUserProfile(_ context.Context, userID string) (*models.User, error) {
	if userID == "missing" {
		return nil, common.UserNotFoundError
	}
	return &models.User{ID: userID, Email: userID + "@college.example", CurrentYear: 2}, nil
}

type yandexTestOAuth struct {
	exchangeErr error
	infoErr     error
	info        yandex.UserInfo
	codes       []string
}

func (o *yandexTestOAuth) AuthorizeURL(state string) string {
	return "https://oauth.yandex.com/authorize?state=" + url.QueryEscape(state)
}

func (o *yandexTestOAuth) ExchangeCode(_ context.Context, code string) (yandex.Token, error) {
	o.codes = append(o.codes, code)
	if o.exchangeErr != nil {
		return yandex.Token{}, o.exchangeErr
	}
	return yandex.Token{AccessToken: "y0_access-" + code, RefreshToken: "1:refresh-" + code, ExpiresIn: 3600}, nil
}

func (o *yandexTestOAuth) UserInfo(context.Context, string) (yandex.UserInfo, error) {
	return o.info, o.infoErr
}

func newYandexTestService(t *testing.T, required bool) (*YandexService, *yandexTestStore, *yandexTestOAuth, *time.Time) {
	t.Helper()
	sealer, err := yandex.NewSealer(yandexTestKey)
	if err != nil {
		t.Fatal(err)
	}
	store := &yandexTestStore{accounts: map[string]*models.YandexAccount{}}
	oauth := &yandexTestOAuth{info: yandex.UserInfo{ID: 1130000000000001, Login: "asha", Email: "asha@yandex.ru"}}
	now := time.UnixMilli(1_800_000_000_000)
	svc := &YandexService{
		stores:   &stores.Storage{YandexAccounts: store, Users: yandexTestUsers{}},
		enabled:  true,
		required: required,
		oauth:    oauth,
		sealer:   sealer,
		stateKey: yandex.StateKey(yandexTestKey),
		now:      func() time.Time { return now },
	}
	return svc, store, oauth, &now
}

func stateFrom(t *testing.T, authorizeURL string) string {
	t.Helper()
	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func TestYandexConnectFlow(t *testing.T) {
	svc, store, _, now := newYandexTestService(t, true)
	ctx := context.Background()

	status, err := svc.Connection(ctx, "user-1")
	if err != nil || !status.Enabled || !status.Required || status.Connected {
		t.Fatalf("before connecting: %+v, %v", status, err)
	}

	if _, err := svc.StartConnection("user-1", "contest-1", false); !errors.Is(err, common.ErrYandexConsentRequired) {
		t.Fatalf("start without consent: %v", err)
	}
	if _, err := svc.StartConnection("user-1", "../admin", true); !errors.Is(err, common.ErrYandexInvalidContest) {
		t.Fatalf("start with unsafe contest id: %v", err)
	}
	consentedAt := now.UnixMilli()
	authorizeURL, err := svc.StartConnection("user-1", "contest-1", true)
	if err != nil {
		t.Fatal(err)
	}
	state := stateFrom(t, authorizeURL)

	*now = now.Add(5 * time.Minute)
	res, contestID, err := svc.CompleteConnection(ctx, "user-1", "7654321", state)
	if err != nil {
		t.Fatal(err)
	}
	if contestID != "contest-1" || !res.Connected || res.TokenState != models.YandexTokenActive || res.YandexLogin != "asha" || res.YandexEmail != "asha@yandex.ru" {
		t.Fatalf("complete: %+v %q", res, contestID)
	}

	saved := store.accounts["user-1"]
	if saved.Email != "user-1@college.example" || saved.YandexUID != 1130000000000001 || saved.ConsentedAt != consentedAt ||
		saved.ConnectedAt != now.UnixMilli() || store.savedAt != now.UnixMilli() {
		t.Fatalf("saved account %+v", saved)
	}
	if saved.TokenExpiresAt != now.Add(time.Hour).UnixMilli() {
		t.Fatalf("token expiry %d must be Unix milliseconds", saved.TokenExpiresAt)
	}
	// recruitment-backup opens these with the same key; they must never be stored in plain text.
	backup, _ := yandex.NewSealer(yandexTestKey)
	if access, err := backup.Open(saved.AccessToken); err != nil || access != "y0_access-7654321" || strings.Contains(saved.AccessToken, "y0_") {
		t.Fatalf("access token %q opens to %q, %v", saved.AccessToken, access, err)
	}
	if refresh, err := backup.Open(saved.RefreshToken); err != nil || refresh != "1:refresh-7654321" {
		t.Fatalf("refresh token opens to %q, %v", refresh, err)
	}
}

func TestYandexCompleteRejections(t *testing.T) {
	ctx := context.Background()

	t.Run("state from another candidate", func(t *testing.T) {
		svc, store, oauth, _ := newYandexTestService(t, false)
		authorizeURL, _ := svc.StartConnection("attacker", "", true)
		_, _, err := svc.CompleteConnection(ctx, "victim", "123", stateFrom(t, authorizeURL))
		if !errors.Is(err, common.ErrYandexInvalidState) || len(oauth.codes) != 0 || len(store.accounts) != 0 {
			t.Fatalf("err %v, exchanged %v, saved %v", err, oauth.codes, store.accounts)
		}
	})

	t.Run("expired state", func(t *testing.T) {
		svc, _, oauth, now := newYandexTestService(t, false)
		authorizeURL, _ := svc.StartConnection("user-1", "", true)
		*now = now.Add(yandex.StateTTL + time.Second)
		if _, _, err := svc.CompleteConnection(ctx, "user-1", "123", stateFrom(t, authorizeURL)); !errors.Is(err, common.ErrYandexInvalidState) || len(oauth.codes) != 0 {
			t.Fatalf("err %v, exchanged %v", err, oauth.codes)
		}
	})

	for _, tc := range []struct {
		name  string
		setup func(*yandexTestStore, *yandexTestOAuth)
		user  string
		want  error
	}{
		{"code refused", func(_ *yandexTestStore, o *yandexTestOAuth) { o.exchangeErr = yandex.ErrInvalidCode }, "user-1", common.ErrYandexInvalidCode},
		{"yandex down", func(_ *yandexTestStore, o *yandexTestOAuth) { o.exchangeErr = errors.New("dial tcp: timeout") }, "user-1", common.ErrYandexUnavailable},
		{"info fails", func(_ *yandexTestStore, o *yandexTestOAuth) { o.infoErr = &yandex.APIError{Op: "info", Status: 500} }, "user-1", common.ErrYandexUnavailable},
		{"account taken", func(s *yandexTestStore, _ *yandexTestOAuth) { s.saveErr = common.ErrYandexAccountTaken }, "user-1", common.ErrYandexAccountTaken},
		{"no profile", func(*yandexTestStore, *yandexTestOAuth) {}, "missing", common.UserNotFoundError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, oauth, _ := newYandexTestService(t, false)
			tc.setup(store, oauth)
			authorizeURL, _ := svc.StartConnection(tc.user, "", true)
			if _, _, err := svc.CompleteConnection(ctx, tc.user, "123", stateFrom(t, authorizeURL)); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestYandexDisabled(t *testing.T) {
	svc := &YandexService{stores: &stores.Storage{YandexAccounts: &yandexTestStore{accounts: map[string]*models.YandexAccount{}}}, now: time.Now}
	status, err := svc.Connection(context.Background(), "user-1")
	if err != nil || status.Enabled || status.Connected {
		t.Fatalf("status %+v, %v", status, err)
	}
	if _, err := svc.StartConnection("user-1", "", true); !errors.Is(err, common.ErrYandexNotConfigured) {
		t.Fatalf("start: %v", err)
	}
	if _, _, err := svc.CompleteConnection(context.Background(), "user-1", "1", "x.y"); !errors.Is(err, common.ErrYandexNotConfigured) {
		t.Fatalf("complete: %v", err)
	}
}

type yandexRegistrationContests struct {
	*stores.ContestStore
	registered []string
}

func (s *yandexRegistrationContests) GetContest(context.Context, string) (*dto.GetContestResponse, error) {
	now := time.Now().UnixMilli()
	return &dto.GetContestResponse{Contest: models.Contest{
		ID: "c1", RegistrationStartTime: now - 60_000, RegistrationEndTime: now + 60_000,
		StartTime: now + 120_000, EndTime: now + 180_000, EligibleTo: []int{2},
	}}, nil
}

func (s *yandexRegistrationContests) RegisterUser(_ context.Context, _ string, userID string) error {
	s.registered = append(s.registered, userID)
	return nil
}

func TestRegistrationNeedsYandexWhenRequired(t *testing.T) {
	for _, required := range []bool{false, true} {
		contests := &yandexRegistrationContests{}
		accounts := &yandexTestStore{accounts: map[string]*models.YandexAccount{"connected": {UserID: "connected"}}}
		svc := NewContestService(&stores.Storage{Contests: contests, Users: yandexTestUsers{}, YandexAccounts: accounts}, nil)
		svc.requireYandex = required

		err := svc.ModifyRegistration(context.Background(), "c1", "not-connected", dto.RegisterAction)
		if required != errors.Is(err, common.ErrYandexNotConnected) || (!required && err != nil) {
			t.Fatalf("required=%v: unconnected candidate got %v", required, err)
		}
		if err := svc.ModifyRegistration(context.Background(), "c1", "connected", dto.RegisterAction); err != nil {
			t.Fatalf("required=%v: connected candidate got %v", required, err)
		}
		want := 2
		if required {
			want = 1
		}
		if len(contests.registered) != want {
			t.Fatalf("required=%v: registered %v", required, contests.registered)
		}
	}
}
