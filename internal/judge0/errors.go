package judge0

import "errors"

var (
	ErrUnavailable         = errors.New("judge0 unavailable")
	ErrInvalidResponse     = errors.New("judge0 returned an invalid response")
	ErrSubmissionNotFound  = errors.New("judge0 submission not found")
	ErrUnsupportedLanguage = errors.New("unsupported language")
)
