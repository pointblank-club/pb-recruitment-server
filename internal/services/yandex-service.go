package services

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/stores"
	"app/internal/yandex"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/labstack/gommon/log"
)

var yandexReturnContestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type yandexOAuth interface {
	AuthorizeURL(state string) string
	ExchangeCode(ctx context.Context, code string) (yandex.Token, error)
	UserInfo(ctx context.Context, accessToken string) (yandex.UserInfo, error)
}

// YandexService connects each candidate's own Yandex account for the backup contest.
// The server stores the account and its sealed OAuth tokens in yandex_accounts;
// recruitment-backup then registers the candidate on the Yandex contest and mirrors their
// submissions with that token.
type YandexService struct {
	stores   *stores.Storage
	enabled  bool
	required bool
	oauth    yandexOAuth
	sealer   *yandex.Sealer
	stateKey []byte
	now      func() time.Time
}

// NewYandexService fails when the configuration is invalid, partial, or required but
// missing, so a misconfigured deployment does not start.
func NewYandexService(storage *stores.Storage) (*YandexService, error) {
	settings, err := yandex.LoadSettings(os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("yandex connect configuration: %w", err)
	}
	svc := &YandexService{stores: storage, required: settings.Required, now: time.Now}
	if !settings.Enabled() {
		return svc, nil
	}
	sealer, err := yandex.NewSealer(settings.TokenKey)
	if err != nil {
		return nil, fmt.Errorf("yandex connect configuration: %w", err)
	}
	svc.enabled = true
	svc.oauth = yandex.NewClient(settings.OAuth)
	svc.sealer = sealer
	svc.stateKey = yandex.StateKey(settings.TokenKey)
	return svc, nil
}

func (s *YandexService) Connection(ctx context.Context, userID string) (*dto.YandexConnectionResponse, error) {
	res := &dto.YandexConnectionResponse{Enabled: s.enabled, Required: s.required}
	account, err := s.stores.YandexAccounts.GetYandexAccount(ctx, userID)
	if errors.Is(err, common.ErrNotFound) {
		return res, nil
	}
	if err != nil {
		return nil, err
	}
	res.Connected = true
	res.TokenState = account.TokenState
	res.YandexLogin = account.YandexLogin
	res.YandexEmail = account.YandexEmail
	res.ConnectedAt = account.ConnectedAt
	return res, nil
}

// StartConnection records the candidate's consent in a signed state and returns the
// Yandex page to send them to.
func (s *YandexService) StartConnection(userID, contestID string, consent bool) (string, error) {
	if !s.enabled {
		return "", common.ErrYandexNotConfigured
	}
	if !consent {
		return "", common.ErrYandexConsentRequired
	}
	if contestID != "" && !yandexReturnContestID.MatchString(contestID) {
		return "", common.ErrYandexInvalidContest
	}
	now := s.now()
	state, err := yandex.SignState(s.stateKey, yandex.State{
		UserID:      userID,
		ContestID:   contestID,
		ConsentedAt: now.UnixMilli(),
		ExpiresAt:   now.Add(yandex.StateTTL).UnixMilli(),
	})
	if err != nil {
		return "", fmt.Errorf("sign yandex state: %w", err)
	}
	return s.oauth.AuthorizeURL(state), nil
}

// CompleteConnection exchanges the code Yandex returned to the website, reads the account
// it belongs to and saves it with its sealed tokens. It returns the contest the
// candidate started from.
func (s *YandexService) CompleteConnection(ctx context.Context, userID, code, rawState string) (*dto.YandexConnectionResponse, string, error) {
	if !s.enabled {
		return nil, "", common.ErrYandexNotConfigured
	}
	state, err := yandex.VerifyState(s.stateKey, rawState, s.now())
	if err != nil || state.UserID != userID {
		return nil, "", common.ErrYandexInvalidState
	}
	user, err := s.stores.Users.GetUserProfile(ctx, userID)
	if err != nil {
		return nil, "", err
	}

	token, err := s.oauth.ExchangeCode(ctx, code)
	if errors.Is(err, yandex.ErrInvalidCode) {
		return nil, "", common.ErrYandexInvalidCode
	}
	if err != nil {
		log.Errorf("yandex code exchange for user %s failed: %v", userID, err)
		return nil, "", fmt.Errorf("%w: %v", common.ErrYandexUnavailable, err)
	}
	info, err := s.oauth.UserInfo(ctx, token.AccessToken)
	if err != nil {
		log.Errorf("yandex account lookup for user %s failed: %v", userID, err)
		return nil, "", fmt.Errorf("%w: %v", common.ErrYandexUnavailable, err)
	}

	access, err := s.sealer.Seal(token.AccessToken)
	if err != nil {
		return nil, "", fmt.Errorf("seal access token: %w", err)
	}
	var refresh string
	if token.RefreshToken != "" {
		if refresh, err = s.sealer.Seal(token.RefreshToken); err != nil {
			return nil, "", fmt.Errorf("seal refresh token: %w", err)
		}
	}
	now := s.now()
	account := &models.YandexAccount{
		UserID:         userID,
		Email:          user.Email,
		YandexUID:      info.ID,
		YandexLogin:    info.Login,
		YandexEmail:    info.Email,
		AccessToken:    access,
		RefreshToken:   refresh,
		TokenExpiresAt: token.ExpiresAt(now),
		TokenState:     models.YandexTokenActive,
		ConsentedAt:    state.ConsentedAt,
		ConnectedAt:    now.UnixMilli(),
	}
	if err := s.stores.YandexAccounts.SaveYandexAccount(ctx, account, now.UnixMilli()); err != nil {
		return nil, "", err
	}
	return &dto.YandexConnectionResponse{
		Enabled:     true,
		Required:    s.required,
		Connected:   true,
		TokenState:  models.YandexTokenActive,
		YandexLogin: info.Login,
		YandexEmail: info.Email,
		ConnectedAt: account.ConnectedAt,
	}, state.ContestID, nil
}
