package stores_test

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/stores"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func insertYandexTestUser(t *testing.T, db *sql.DB, id, usn string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users(id, name, email, usn, mobile_number, current_year, department)
		VALUES ($1, 'Candidate', $1 || '@college.example', $2, '9876543210', 2, 'CS')`, id, usn); err != nil {
		t.Fatal(err)
	}
}

func TestYandexAccountStorePostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	insertYandexTestUser(t, db, "u-1", "1DS24CS001")
	insertYandexTestUser(t, db, "u-2", "1DS24CS002")
	store := stores.NewYandexAccountStore(db)

	if _, err := store.GetYandexAccount(ctx, "u-1"); !errors.Is(err, common.ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}
	if ok, err := store.HasYandexAccount(ctx, "u-1"); err != nil || ok {
		t.Fatalf("HasYandexAccount = %v, %v", ok, err)
	}

	first := &models.YandexAccount{UserID: "u-1", Email: "u-1@college.example", YandexUID: 1001, YandexLogin: "",
		YandexEmail: "asha@gmail.com", AccessToken: "v1:a1", RefreshToken: "v1:r1", TokenExpiresAt: 5_000, ConsentedAt: 10, ConnectedAt: 11}
	if err := store.SaveYandexAccount(ctx, first, 11); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetYandexAccount(ctx, "u-1")
	if err != nil || got.YandexUID != 1001 || got.YandexLogin != "" || got.YandexEmail != "asha@gmail.com" ||
		got.TokenState != models.YandexTokenActive || got.ConsentedAt != 10 || got.ConnectedAt != 11 {
		t.Fatalf("GetYandexAccount = %+v, %v", got, err)
	}

	// The backup flags a broken token and records upkeep; a reconnect must clear all of it.
	if _, err := db.Exec(`UPDATE yandex_accounts SET token_state = 'needs_reauth', last_error = '401',
		last_refreshed_at = 1, last_verified_at = 1 WHERE user_id = 'u-1'`); err != nil {
		t.Fatal(err)
	}
	reconnect := &models.YandexAccount{UserID: "u-1", Email: "u-1@college.example", YandexUID: 1002, YandexLogin: "asha",
		AccessToken: "v1:a2", ConsentedAt: 20, ConnectedAt: 21}
	if err := store.SaveYandexAccount(ctx, reconnect, 21); err != nil {
		t.Fatal(err)
	}
	var (
		uid                                   int64
		state, access                         string
		refresh, yEmail, lastErr              sql.NullString
		expires, refreshedAt, verifiedAt, upd sql.NullInt64
	)
	if err := db.QueryRow(`SELECT yandex_uid, token_state, access_token, refresh_token, yandex_email, last_error,
		token_expires_at, last_refreshed_at, last_verified_at, updated_at FROM yandex_accounts WHERE user_id = 'u-1'`).
		Scan(&uid, &state, &access, &refresh, &yEmail, &lastErr, &expires, &refreshedAt, &verifiedAt, &upd); err != nil {
		t.Fatal(err)
	}
	if uid != 1002 || state != "active" || access != "v1:a2" || refresh.Valid || yEmail.Valid || lastErr.Valid ||
		expires.Valid || refreshedAt.Valid || verifiedAt.Valid || upd.Int64 != 21 {
		t.Fatalf("reconnect left stale state: uid=%d state=%s access=%s refresh=%v email=%v err=%v exp=%v ref=%v ver=%v upd=%v",
			uid, state, access, refresh, yEmail, lastErr, expires, refreshedAt, verifiedAt, upd)
	}

	// u-1 left 1001 behind, so u-2 may take it, but not u-1's current account.
	taken := &models.YandexAccount{UserID: "u-2", Email: "u-2@college.example", YandexUID: 1002, AccessToken: "v1:x", ConsentedAt: 1, ConnectedAt: 1}
	if err := store.SaveYandexAccount(ctx, taken, 1); !errors.Is(err, common.ErrYandexAccountTaken) {
		t.Fatalf("connecting another candidate's Yandex account: %v", err)
	}
	taken.YandexUID = 1001
	if err := store.SaveYandexAccount(ctx, taken, 1); err != nil {
		t.Fatalf("connecting a released Yandex account: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM users WHERE id = 'u-2'`); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.HasYandexAccount(ctx, "u-2"); ok {
		t.Fatal("deleting a user must delete their Yandex connection")
	}
}

// recruitment-backup reads and updates yandex_accounts with these statements (store/tokens.go
// and store/participants.go there). They must keep working against this migration.
func TestYandexAccountsMatchRecruitmentBackupPostgres(t *testing.T) {
	db := postgresTestDB(t)
	insertYandexTestUser(t, db, "u-1", "1DS24CS001")
	store := stores.NewYandexAccountStore(db)
	if err := store.SaveYandexAccount(context.Background(), &models.YandexAccount{UserID: "u-1", Email: "u-1@college.example",
		YandexUID: 1130000000000001, YandexLogin: "asha", AccessToken: "v1:a1", RefreshToken: "v1:r1", TokenExpiresAt: 5_000,
		ConsentedAt: 1, ConnectedAt: 1}, 1); err != nil {
		t.Fatal(err)
	}

	var userID, login, sealedAccess, sealedRefresh string
	var expiresAt int64
	if err := db.QueryRow(`
		SELECT a.user_id, a.yandex_login, a.access_token, COALESCE(a.refresh_token, ''), COALESCE(a.token_expires_at, 0)
		FROM yandex_accounts a
		WHERE a.refresh_token IS NOT NULL AND a.token_state = 'active'
		  AND a.token_expires_at IS NOT NULL AND a.token_expires_at < $1
		ORDER BY a.token_expires_at
		LIMIT $2`, 6_000, 10).Scan(&userID, &login, &sealedAccess, &sealedRefresh, &expiresAt); err != nil {
		t.Fatalf("backup refresh query: %v", err)
	}
	if userID != "u-1" || sealedAccess != "v1:a1" || sealedRefresh != "v1:r1" || expiresAt != 5_000 {
		t.Fatalf("backup read %s %s %s %s %d", userID, login, sealedAccess, sealedRefresh, expiresAt)
	}

	res, err := db.Exec(`
		UPDATE yandex_accounts
		SET access_token = $3, refresh_token = COALESCE($4, refresh_token), token_expires_at = $5,
		    token_state = 'active', last_refreshed_at = $6, last_error = NULL, updated_at = $6
		WHERE user_id = $1 AND access_token = $2`, "u-1", "v1:a1", "v1:a2", nil, 9_000, 2)
	if n, _ := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("backup token save: %v, rows %d", err, n)
	}
	if _, err := db.Exec(`UPDATE yandex_accounts SET token_state = 'needs_reauth', last_error = $3
		WHERE user_id = $1 AND access_token = $2`, "u-1", "v1:a2", "invalid_grant"); err != nil {
		t.Fatalf("backup reauth flag: %v", err)
	}
	if _, err := db.Exec(`UPDATE yandex_accounts SET token_state = 'active', last_error = NULL, last_verified_at = $3
		WHERE user_id = $1 AND access_token = $2`, "u-1", "v1:a2", 3); err != nil {
		t.Fatalf("backup verify: %v", err)
	}
	var uid int64
	if err := db.QueryRow(`SELECT a.yandex_uid FROM yandex_accounts a WHERE a.user_id = $1`, "u-1").Scan(&uid); err != nil || uid != 1130000000000001 {
		t.Fatalf("backup registrar uid: %d, %v", uid, err)
	}
}
