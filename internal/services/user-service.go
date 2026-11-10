package services

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/stores"
	"context"
	"fmt"
	"log"

	"firebase.google.com/go/v4/auth"
)

type UserService struct {
	stores     *stores.Storage
	authClient *auth.Client
}

func NewUserService(stores *stores.Storage, authClient *auth.Client) *UserService {
	return &UserService{stores, authClient}
}

func (us *UserService) CreateUser(ctx context.Context, userID string, req *dto.CreateUserRequest) error {
	user, err := us.authClient.GetUser(ctx, userID)
	if err != nil {
		log.Printf("user-service: error fetching user: %v", err)
		return fmt.Errorf("error fetching user: %v", err)
	}

	return us.stores.Users.CreateUser(ctx, user, req)
}

func (us *UserService) GetUserProfile(ctx context.Context, userID string) (*models.User, error) {
	return us.stores.Users.GetUserProfile(ctx, userID)
}

func (us *UserService) UpdateUserProfile(ctx context.Context, userID string, req *dto.UpdateUserProfileRequest) error {
	return us.stores.Users.UpdateUserProfile(ctx, userID, req)
}

func (us *UserService) Signup(ctx context.Context, req *dto.SignupRequest) (*dto.SignupResponse, error) {
	// Create Firebase user first
	userRecord, err := us.authClient.CreateUser(ctx, (&auth.UserToCreate{}).
		Email(req.Email).
		Password(req.Password).
		DisplayName(req.Name))
	if err != nil {
		if auth.IsEmailAlreadyExists(err) {
			return nil, common.EmailAlreadyExistsError
		}
		if auth.IsUIDAlreadyExists(err) {
			return nil, common.UserAlreadyExistsError
		}
		return nil, fmt.Errorf("firebase create user: %w", err)
	}

	// Create user in database using Firebase UID
	createReq := &dto.CreateUserRequest{
		Name:         req.Name,
		USN:          req.USN,
		MobileNumber: req.MobileNumber,
		CurrentYear:  req.CurrentYear,
		Department:   req.Department,
	}

	if err := us.stores.Users.CreateUser(ctx, userRecord, createReq); err != nil {
		// If DB insert fails, clean up Firebase user
		if delErr := us.authClient.DeleteUser(ctx, userRecord.UID); delErr != nil {
			log.Printf("user-service: cleanup firebase user %s failed: %v", userRecord.UID, delErr)
		}
		return nil, err
	}

	return &dto.SignupResponse{
		UserID: userRecord.UID,
	}, nil
}
