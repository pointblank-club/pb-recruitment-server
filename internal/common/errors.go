package common

import "errors"

var (
	ErrNotFound                    = errors.New("Resource not found")
	UserAlreadyExistsError         = errors.New("user already exists")
	USNAlreadyExistsError          = errors.New("USN or Application Number is already registered")
	EmailAlreadyExistsError        = errors.New("email is already registered")
	UserNotFoundError              = errors.New("user not found")
	CreateUserFailedError          = errors.New("failed to create user")
	FetchUserFailedError           = errors.New("failed to fetch user profile")
	UpdateUserFailedError          = errors.New("failed to update user profile")
	ProfileUpdateFailedError       = errors.New("user profile update failed")
	InvalidApplicationNumberError  = errors.New("first-year students must provide a valid Application Number (e.g., 26UGDS1234)")
	InvalidUSNError                = errors.New("provide a valid USN (e.g., 1DS24IC015)")
	InvalidUSNBranchError          = errors.New("USN does not match the selected department")
	InvalidMobileNumberError       = errors.New("invalid mobile number format")
	USNYearImmutableError          = errors.New("USN and Year cannot be changed")
	ContestNotFoundError           = errors.New("contest not found")
	FetchContestFailedError        = errors.New("failed to fetch contest")
	UserNotRegisteredError         = errors.New("user not registered")
	ContestNotRunningError         = errors.New("contest is not running")
	ContestRegistrationClosedError = errors.New("contest registration is closed")
	InvalidYearError               = errors.New("you are not eligible for this contest based on your academic year")
	KeyNotFoundError               = errors.New("key not found")
	KeyAlreadyExistsError          = errors.New("key already exists")
	ErrIdempotencyConflict         = errors.New("request ID already used for a different submission")
	ErrSubmissionAlreadyExists     = errors.New("submission already exists")
	ErrUnsupportedLanguage         = errors.New("unsupported language")
	ErrNoTestcases                 = errors.New("problem has no testcases")
	ErrInvalidCode                 = errors.New("code must be non-empty UTF-8 text up to 64 KiB")
	ErrInvalidAnswer               = errors.New("MCQ answer must refer to an existing option")
	ErrProblemsLocked              = errors.New("problems cannot be edited or deleted after the contest starts")
	ErrContestStartTimeLocked      = errors.New("start time cannot be changed after the contest starts")
	ErrRateLimitExceeded           = errors.New("rate limit exceeded, please wait before submitting again")
	ErrContestNotClosed            = errors.New("contest has not closed yet")
	ErrPendingSubmissionsExist     = errors.New("pending submissions exist for contest")
	ErrContestNotStarted           = errors.New("contest has not started yet")
	ErrReportNotFound              = errors.New("plagiarism report not found")
	ErrDolosUnavailable            = errors.New("plagiarism analysis is not configured")
	ErrReportNotReady              = errors.New("plagiarism report is not finished yet")
	ErrPairNotFound                = errors.New("plagiarism pair not found in report")
	ErrInvalidPairKey              = errors.New("invalid pair key")
	ErrTabSwitchTerminated         = errors.New("contest participation terminated due to tab switching")
	ErrInvalidTabSwitchRequest     = errors.New("invalid tab switch payload")
	ErrCannotUnregisterAfterStart  = errors.New("cannot unregister after contest has started")
	ErrContestSessionEnded         = errors.New("contest session already ended")
)

// TabSwitchStaleGenerationError indicates that a tab-switch observation belongs
// to an older reset generation and carries the authoritative registration state.
type TabSwitchStaleGenerationError struct {
	TabSwitchCount int
	IsTerminated   bool
	Generation     int
}

func (e *TabSwitchStaleGenerationError) Error() string {
	return "stale tab switch generation"
}

// SubmissionCooldownError carries the remaining database-clock cooldown.
type SubmissionCooldownError struct{ RetryAfter int64 }

func (e *SubmissionCooldownError) Error() string { return ErrRateLimitExceeded.Error() }
func (e *SubmissionCooldownError) Unwrap() error { return ErrRateLimitExceeded }
