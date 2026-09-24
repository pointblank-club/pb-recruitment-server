package common

import "errors"

var (
	ErrNotFound                    = errors.New("Resource not found")
	UserAlreadyExistsError         = errors.New("user already exists")
	UserNotFoundError              = errors.New("user not found")
	CreateUserFailedError          = errors.New("failed to create user")
	FetchUserFailedError           = errors.New("failed to fetch user profile")
	UpdateUserFailedError          = errors.New("failed to update user profile")
	ProfileUpdateFailedError       = errors.New("user profile update failed")
	InvalidApplicationNumberError  = errors.New("first-year students must provide a valid Application Number (e.g., 26UGDS1234)")
	InvalidUSNError                = errors.New("provide a valid USN (e.g., 1DS24IC015)")
	InvalidMobileNumberError       = errors.New("invalid mobile number format")
	USNYearImmutableError          = errors.New("USN and Year cannot be changed")
	ContestNotFoundError           = errors.New("contest not found")
	FetchContestFailedError        = errors.New("failed to fetch contest")
	UserNotRegisteredError         = errors.New("user not registered")
	ContestNotRunningError         = errors.New("contest is not running")
	ContestRegistrationClosedError = errors.New("contest registration is closed")
	InvalidYearError               = errors.New("invalid year")
	KeyNotFoundError               = errors.New("key not found")
	KeyAlreadyExistsError          = errors.New("key already exists")
)
