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

// Migration 000051 creates recruitment-backup's own tables. These are the statements it
// runs on them (store/participants.go, store/contest.go and store/mirrors.go there),
// joined with this server's real tables. They must keep working against these migrations.
func TestBackupTablesMatchRecruitmentBackupPostgres(t *testing.T) {
	db := postgresTestDB(t)
	exec := func(q string, args ...any) sql.Result {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
		return res
	}
	const contest, problem, user, submission = "c-backup", "p-backup", "u-1", "s-1"
	exec(`INSERT INTO contests(id,name,description,registration_start_time,registration_end_time,start_time,end_time)
		VALUES($1,'Backup','',1,2,3,9000000000000)`, contest)
	exec(`INSERT INTO problems(id,contest_id,name,score,type) VALUES($1,$2,'A',100,'code')`, problem, contest)
	insertYandexTestUser(t, db, user, "1DS24CS001")
	exec(`INSERT INTO contest_registrations(contest_id,user_id,registered_at) VALUES($1,$2,10)`, contest, user)
	if err := stores.NewYandexAccountStore(db).SaveYandexAccount(context.Background(), &models.YandexAccount{UserID: user,
		Email: "u-1@college.example", YandexUID: 1130000000000001, YandexLogin: "", AccessToken: "v1:a1",
		ConsentedAt: 1, ConnectedAt: 1}, 1); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO submissions(id,user_id,contest_id,problem_id,type,language,status,created_at)
		VALUES($1,$2,$3,$4,'code','cpp','pending',20)`, submission, user, contest, problem)

	// Registrar: candidates registered here with a connected account and no Yandex participant.
	var pendingUser, pendingLogin string
	var pendingUID int64
	if err := db.QueryRow(`
		SELECT cr.user_id, a.yandex_uid, a.yandex_login
		FROM contest_registrations cr
		JOIN yandex_accounts a ON a.user_id = cr.user_id
		LEFT JOIN yandex_participants p ON p.contest_id = cr.contest_id AND p.user_id = cr.user_id
		WHERE cr.contest_id = $1
		  AND (p.user_id IS NULL OR p.participant_id IS NULL OR p.yandex_uid <> a.yandex_uid)
		ORDER BY (p.last_error IS NOT NULL), cr.registered_at
		LIMIT $2`, contest, 50).Scan(&pendingUser, &pendingUID, &pendingLogin); err != nil {
		t.Fatalf("backup pending registrations: %v", err)
	}
	if pendingUser != user || pendingUID != 1130000000000001 || pendingLogin != "" {
		t.Fatalf("pending registration %s %d %q", pendingUser, pendingUID, pendingLogin)
	}
	exec(`
		INSERT INTO yandex_participants AS p
		    (contest_id, user_id, yandex_uid, yandex_login, participant_id, registered_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
		ON CONFLICT (contest_id, user_id) DO UPDATE SET
		    started_at = CASE WHEN p.yandex_uid = EXCLUDED.yandex_uid AND p.participant_id = EXCLUDED.participant_id
		                      THEN p.started_at END,
		    yandex_uid = EXCLUDED.yandex_uid,
		    yandex_login = EXCLUDED.yandex_login,
		    participant_id = EXCLUDED.participant_id,
		    registered_at = EXCLUDED.registered_at,
		    last_error = NULL`, contest, user, int64(1130000000000001), "", int64(4242), int64(30))
	if res := exec(`
		UPDATE yandex_participants SET started_at = $4, last_error = $5
		WHERE contest_id = $1 AND user_id = $2 AND participant_id = $3 AND started_at IS NULL`,
		contest, user, int64(4242), int64(40), nil); rowsAffected(t, res) != 1 {
		t.Fatal("backup start did not update the participant")
	}

	// Enqueue and claim: the send queue joins submissions, participants and accounts.
	if res := exec(`
		INSERT INTO submission_mirrors (submission_id, contest_id, user_id, state, queued_at, next_attempt_at)
		SELECT s.id, s.contest_id, s.user_id, 'queued', $2, $2
		FROM submissions s
		JOIN yandex_participants p ON p.contest_id = s.contest_id AND p.user_id = s.user_id
		WHERE s.contest_id = $1 AND s.type IN ('code', 'mcq')
		  AND NOT EXISTS (SELECT 1 FROM submission_mirrors sm WHERE sm.submission_id = s.id)
		ORDER BY s.created_at
		LIMIT $3
		ON CONFLICT (submission_id) DO NOTHING`, contest, int64(50), 500); rowsAffected(t, res) != 1 {
		t.Fatal("backup enqueue did not queue the submission")
	}
	var claimedID, claimedUser, problemID, kind, language, token string
	var claimedAt, firstAttempt, createdAt int64
	var attempts int
	var choices any
	if err := db.QueryRow(`
		WITH picked AS (
		    SELECT sm.submission_id
		    FROM submission_mirrors sm
		    JOIN yandex_participants p ON p.contest_id = sm.contest_id AND p.user_id = sm.user_id
		    JOIN yandex_accounts a ON a.user_id = sm.user_id
		    JOIN submissions s ON s.id = sm.submission_id
		    WHERE sm.state IN ('queued', 'failed')
		      AND sm.next_attempt_at <= $1
		      AND p.participant_id IS NOT NULL
		      AND p.started_at IS NOT NULL
		      AND p.yandex_uid = a.yandex_uid
		      AND a.token_state = 'active'
		      AND NOT EXISTS (
		          SELECT 1 FROM submission_mirrors prev
		          JOIN submissions ps ON ps.id = prev.submission_id
		          WHERE prev.contest_id = sm.contest_id AND prev.user_id = sm.user_id
		            AND prev.state IN ('queued', 'failed', 'sending', 'uncertain')
		            AND (ps.created_at, ps.id) < (s.created_at, s.id))
		    ORDER BY s.created_at
		    LIMIT $2
		    FOR UPDATE OF sm SKIP LOCKED
		)
		UPDATE submission_mirrors sm
		SET state = 'sending',
		    attempts = sm.attempts + 1,
		    claimed_at = $1,
		    first_attempted_at = COALESCE(sm.first_attempted_at, $1)
		FROM picked, submissions s, yandex_accounts a
		WHERE sm.submission_id = picked.submission_id
		  AND s.id = sm.submission_id
		  AND a.user_id = sm.user_id
		RETURNING sm.submission_id, sm.user_id, sm.claimed_at, sm.attempts,
		          s.problem_id, s.type::text, COALESCE(s.language, ''), COALESCE(s.choices, '{}'),
		          a.access_token, sm.first_attempted_at, s.created_at`, int64(60), 50).Scan(
		&claimedID, &claimedUser, &claimedAt, &attempts, &problemID, &kind, &language, &choices, &token, &firstAttempt, &createdAt); err != nil {
		t.Fatalf("backup claim: %v", err)
	}
	if claimedID != submission || attempts != 1 || kind != "code" || language != "cpp" || token != "v1:a1" || createdAt != 20 {
		t.Fatalf("claimed %s %d %s %s %s %d", claimedID, attempts, kind, language, token, createdAt)
	}
	exec(`UPDATE submission_mirrors SET state = 'sent', yandex_submission_id = $2, sent_at = $3 WHERE submission_id = $1`,
		submission, int64(777), int64(70))

	// Constraints the backup relies on: one mirror per Yandex run, mirrors only for known
	// participants, and only the states it uses.
	exec(`INSERT INTO submissions(id,user_id,contest_id,problem_id,type,status,created_at) VALUES('s-2',$1,$2,$3,'code','pending',21)`,
		user, contest, problem)
	exec(`INSERT INTO submission_mirrors (submission_id, contest_id, user_id, queued_at, next_attempt_at) VALUES('s-2',$1,$2,1,1)`, contest, user)
	for name, q := range map[string]string{
		"duplicate Yandex run": `UPDATE submission_mirrors SET yandex_submission_id = 777 WHERE submission_id = 's-2'`,
		"unknown participant":  `INSERT INTO submission_mirrors (submission_id, contest_id, user_id, queued_at, next_attempt_at) VALUES('s-3','c-backup','nobody',1,1)`,
		"unknown mirror state": `UPDATE submission_mirrors SET state = 'lost' WHERE submission_id = 's-2'`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func rowsAffected(t *testing.T, res sql.Result) int64 {
	t.Helper()
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	return n
}
