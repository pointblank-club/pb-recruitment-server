package stores

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"

	"firebase.google.com/go/v4/auth"
	"github.com/jackc/pgx/v5/pgconn"
)

type UserStore struct {
	db *sql.DB
}

func NewUserStore(db *sql.DB) *UserStore {
	return &UserStore{
		db: db,
	}
}

func (us *UserStore) CreateUser(ctx context.Context, user *auth.UserRecord, req *dto.CreateUserRequest) error {
	if us == nil || us.db == nil {
		return fmt.Errorf("user store: db is not initialized")
	}

	normalizedUSN := strings.ToUpper(strings.TrimSpace(req.USN))
	normalizedMobile := strings.TrimSpace(req.MobileNumber)

	const q = `
	INSERT INTO users (id, name, email, usn, mobile_number, current_year, department)
	VALUES ($1, $2, $3, $4, $5, $6, $7)
	ON CONFLICT (id) DO NOTHING
	`

	res, err := us.db.ExecContext(ctx, q, user.UID, req.Name, user.Email, normalizedUSN, normalizedMobile, req.CurrentYear, req.Department)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			switch pgErr.ConstraintName {
			case "users_usn_key":
				return common.USNAlreadyExistsError
			case "users_email_key":
				return common.EmailAlreadyExistsError
			case "users_pkey":
				return common.UserAlreadyExistsError
			}
		}

		log.Printf("user-store: insert error %v", err)
		return fmt.Errorf("insert error: %w", err)
	}

	// If rows affected is 0, then the user already exists
	affected, err := res.RowsAffected()
	if err != nil {
		log.Printf("user-store: rows error %v", err)
		return fmt.Errorf("rows error: %w", err)
	}

	if affected == 0 {
		return common.UserAlreadyExistsError
	}

	return nil
}

func (us *UserStore) GetUserProfile(ctx context.Context, userID string) (*models.User, error) {
	if us == nil || us.db == nil {
		return nil, fmt.Errorf("db is not initialized")
	}

	const q = `
	SELECT id, name, email, usn, mobile_number, current_year, department
	FROM users
	WHERE id = $1
	`

	row := us.db.QueryRowContext(ctx, q, userID)

	var user models.User
	if err := row.Scan(&user.ID, &user.Name, &user.Email, &user.USN, &user.MobileNumber, &user.CurrentYear, &user.Department); err != nil {
		if err == sql.ErrNoRows {
			return nil, common.UserNotFoundError
		}
		log.Printf("user-store: row error %v", err)
		return nil, fmt.Errorf("row error: %w", err)
	}

	return &user, nil
}

func (us *UserStore) UpdateUserProfile(ctx context.Context, userID string, req *dto.UpdateUserProfileRequest) error {
	if us == nil || us.db == nil {
		return fmt.Errorf("contest store: db is not initialized")
	}

	const q = `
	UPDATE users
	SET name = $2, mobile_number = $3, department = $4
	WHERE id = $1
	`

	res, err := us.db.ExecContext(ctx, q, userID, req.Name, req.MobileNumber, req.Department)
	if err != nil {
		log.Printf("user-store: update error %v", err)
		return fmt.Errorf("update error: %w", err)
	}

	// If rows affected is 0, then the user does not exist
	affected, err := res.RowsAffected()
	if err != nil {
		log.Printf("user-store: rows error %v", err)
		return fmt.Errorf("rows error: %w", err)
	}

	if affected == 0 {
		return common.UserNotFoundError
	}

	return nil
}

