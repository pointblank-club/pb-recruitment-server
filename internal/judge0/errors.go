package judge0

import "errors"

var (
	ErrQueueFull           = errors.New("Judge0 queue is full")
	ErrDispatchRejected    = errors.New("Judge0 explicitly rejected dispatch")
	ErrDispatchNotSent     = errors.New("Judge0 dispatch was not sent")
	ErrUnavailable         = errors.New("judge0 unavailable")
	ErrInvalidResponse     = errors.New("judge0 returned an invalid response")
	ErrUnsupportedLanguage = errors.New("unsupported language")
)
