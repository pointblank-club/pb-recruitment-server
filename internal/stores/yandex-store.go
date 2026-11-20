package stores

import (
	"app/internal/common"
	"app/internal/models"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// YandexAccountStore owns yandex_accounts. recruitment-backup also writes this table, but
// only its token columns, and only while the stored access token is the one it read, so a
// reconnect saved here always wins over a concurrent refresh there.
type YandexAccountStore struct {
	db *sql.DB
}

func NewYandexAccountStore(db *sql.DB) *YandexAccountStore {
	return &YandexAccountStore{db: db}
}

// GetYandexAccount returns the candidate's connection without its tokens, or
// common.ErrNotFound when they have not connected.
func (s *YandexAccountStore) GetYandexAccount(ctx context.Context, userID string) (*models.YandexAccount, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("yandex account store: db is not initialized")
	}
	const q = `
		SELECT user_id, email, yandex_uid, yandex_login, COALESCE(yandex_email, ''),
		       token_state, consented_at, connected_at
		FROM yandex_accounts
		WHERE user_id = $1`
	var a models.YandexAccount
	err := s.db.QueryRowContext(ctx, q, userID).Scan(&a.UserID, &a.Email, &a.YandexUID, &a.YandexLogin,
		&a.YandexEmail, &a.TokenState, &a.ConsentedAt, &a.ConnectedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, common.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get yandex account: %w", err)
	}
	return &a, nil
}

func (s *YandexAccountStore) HasYandexAccount(ctx context.Context, userID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("yandex account store: db is not initialized")
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM yandex_accounts WHERE user_id = $1)`, userID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check yandex account: %w", err)
	}
	return exists, nil
}

// SaveYandexAccount connects or reconnects the candidate's account. A reconnect replaces
// every account and token column and clears the backup's upkeep state, since the new
// token may belong to a different Yandex account. A Yandex account already connected to
// another candidate is refused with common.ErrYandexAccountTaken.
func (s *YandexAccountStore) SaveYandexAccount(ctx context.Context, a *models.YandexAccount, now int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("yandex account store: db is not initialized")
	}
	const q = `
		INSERT INTO yandex_accounts (user_id, email, yandex_uid, yandex_login, yandex_email, access_token,
		                             refresh_token, token_expires_at, token_state, consented_at, connected_at, updated_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, NULLIF($7, ''), NULLIF($8, 0), 'active', $9, $10, $11)
		ON CONFLICT (user_id) DO UPDATE SET
		    email = EXCLUDED.email,
		    yandex_uid = EXCLUDED.yandex_uid,
		    yandex_login = EXCLUDED.yandex_login,
		    yandex_email = EXCLUDED.yandex_email,
		    access_token = EXCLUDED.access_token,
		    refresh_token = EXCLUDED.refresh_token,
		    token_expires_at = EXCLUDED.token_expires_at,
		    token_state = 'active',
		    last_error = NULL,
		    last_refreshed_at = NULL,
		    last_verified_at = NULL,
		    consented_at = EXCLUDED.consented_at,
		    connected_at = EXCLUDED.connected_at,
		    updated_at = EXCLUDED.updated_at`
	_, err := s.db.ExecContext(ctx, q, a.UserID, a.Email, a.YandexUID, a.YandexLogin, a.YandexEmail, a.AccessToken,
		a.RefreshToken, a.TokenExpiresAt, a.ConsentedAt, a.ConnectedAt, now)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "yandex_accounts_yandex_uid_key" {
		return common.ErrYandexAccountTaken
	}
	if err != nil {
		return fmt.Errorf("save yandex account: %w", err)
	}
	return nil
}
