package judge0

import "errors"

var (
	ErrDispatchRejected       = errors.New("Judge0 explicitly rejected dispatch")
	ErrDispatchNotSent        = errors.New("Judge0 dispatch was not attempted")
	ErrIdempotencyUnsupported = errors.New("Judge0 did not honor the required idempotency contract")
	ErrUnavailable            = errors.New("judge0 unavailable")
	ErrInvalidResponse        = errors.New("judge0 returned an invalid response")
	ErrUnsupportedLanguage    = errors.New("unsupported language")
)
