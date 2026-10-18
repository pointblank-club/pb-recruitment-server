package services

import (
	"app/internal/common"
	"app/internal/judge0"
	"app/internal/models/dto"
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalidRun = errors.New("invalid run: supported language, UTF-8 code (up to 64 KiB) and stdin (up to 64 KiB) required")

func (ss *SubmissionService) Run(ctx context.Context, userID string, req dto.RunRequest) (*dto.RunResponse, error) {
	if userID == "" {
		return nil, ErrInvalidRun
	}
	if len(req.Code) > 64<<10 || len(req.Stdin) > 64<<10 || !utf8.ValidString(req.Code) || !utf8.ValidString(req.Stdin) {
		return nil, ErrInvalidRun
	}
	if strings.TrimSpace(req.Code) == "" {
		return nil, ErrInvalidRun
	}
	language, err := judge0.LanguageID(req.Language)
	if err != nil {
		return nil, ErrInvalidRun
	}
	// shortcut: stop waiting after 15 seconds; add owned run tokens if the UI needs later polling.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Reuse the atomic PostgreSQL limiter with a separate namespace from submits.
	if ss.stores == nil || ss.stores.Submissions == nil {
		return nil, judge0.ErrUnavailable
	}
	if err := ss.stores.Submissions.CheckSubmissionRateLimit(ctx, "run:user:"+userID, 5); err != nil {
		return nil, err
	}
	if err := ss.stores.Submissions.CheckRunCapacity(ctx); err != nil {
		return nil, err
	}
	results, err := ss.judge0.CreateBatch(ctx, []judge0.SubmissionRequest{{
		SourceCode: req.Code, LanguageID: language, Stdin: req.Stdin, CPUTimeLimit: 2, WallTimeLimit: 5,
		MemoryLimit: 256 * 1024, MaxFileSize: 64, CompilerOptions: judge0.CompilerOptions(language),
	}})
	if errors.Is(err, judge0.ErrQueueFull) {
		return nil, common.ErrRateLimitExceeded
	}
	if err != nil {
		return nil, err
	}
	if len(results) != 1 || results[0].Token == "" {
		return nil, judge0.ErrUnavailable
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		statuses, err := ss.judge0.GetSubmissionsWithOutput(ctx, []string{results[0].Token})
		for errors.Is(err, judge0.ErrUnavailable) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ticker.C:
				statuses, err = ss.judge0.GetSubmissionsWithOutput(ctx, []string{results[0].Token})
			}
		}
		if err != nil {
			return nil, err
		}
		if statuses[0] == nil {
			return nil, judge0.ErrUnavailable
		}
		result := statuses[0]
		status, final := mapJudgeStatus(result.Status.ID)
		if final {
			runtime, err := judgeRuntimeMillis(result.Time)
			if err != nil {
				return nil, judge0.ErrInvalidResponse
			}
			memory := int64(0)
			if result.Memory != nil {
				memory = *result.Memory
			}
			stdout, stdoutErr := decodeJudgeOutput(result.Stdout)
			stderr, stderrErr := decodeJudgeOutput(result.Stderr)
			compileOutput, compileErr := decodeJudgeOutput(result.CompileOutput)
			if stdoutErr != nil || stderrErr != nil || compileErr != nil {
				return nil, judge0.ErrInvalidResponse
			}
			return &dto.RunResponse{Status: status, Stdout: stdout, Stderr: stderr, CompileOutput: compileOutput, Runtime: runtime, Memory: memory}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
