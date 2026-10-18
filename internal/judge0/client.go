package judge0

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/gommon/log"
)

type Client struct {
	baseURL        string
	authToken      string
	callbackBase   string
	callbackSecret string
	httpClient     *http.Client
}

// Dispatch leaves ten seconds for DB bookkeeping within the browser's thirty-second timeout.
const maxDispatchTimeout = 10 * time.Second

// shortcut: covers two stock Judge0 4 MiB files with Base64/JSON; raise if provider file limits grow.
const maxResultBytesPerSubmission = 12 << 20

func NewClient() *Client {
	callbackBase, callbackSecret := strings.TrimRight(os.Getenv("JUDGE0_CALLBACK_BASE_URL"), "/"), os.Getenv("JUDGE0_CALLBACK_SECRET")
	if (callbackBase == "") != (callbackSecret == "") {
		log.Errorf("Judge0 callbacks are misconfigured: JUDGE0_CALLBACK_BASE_URL and JUDGE0_CALLBACK_SECRET must be set together")
	}
	timeout := maxDispatchTimeout
	if raw := os.Getenv("JUDGE0_TIMEOUT_MS"); raw != "" {
		if ms, err := time.ParseDuration(raw + "ms"); err == nil && ms > 0 {
			timeout = min(ms, maxDispatchTimeout)
		}
	}

	return &Client{
		baseURL:        strings.TrimRight(os.Getenv("JUDGE0_URL"), "/"),
		authToken:      os.Getenv("JUDGE0_AUTH_TOKEN"),
		callbackBase:   callbackBase,
		callbackSecret: callbackSecret,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *Client) Timeout() time.Duration {
	if c == nil || c.httpClient == nil || c.httpClient.Timeout <= 0 {
		return maxDispatchTimeout
	}
	return min(c.httpClient.Timeout, maxDispatchTimeout)
}

func (c *Client) CallbackURL(executionID string, attempt ...string) string {
	if c.callbackBase == "" || c.callbackSecret == "" {
		return ""
	}
	query := url.Values{"sig": {c.CallbackSignature(executionID, attempt...)}}
	if len(attempt) > 0 && attempt[0] != "" {
		query.Set("attempt", attempt[0])
	}
	return fmt.Sprintf("%s/internal/judge0/callback/%s?%s", c.callbackBase, executionID, query.Encode())
}

func (c *Client) CallbacksEnabled() bool {
	return c != nil && c.callbackBase != "" && c.callbackSecret != ""
}
func (c *Client) CallbackSignature(id string, attempt ...string) string {
	if parsed, err := uuid.Parse(id); err == nil {
		id = parsed.String()
	}
	if len(attempt) > 0 && attempt[0] != "" {
		id += "/" + attempt[0]
	}
	m := hmac.New(sha256.New, []byte(c.callbackSecret))
	_, _ = m.Write([]byte(id))
	return hex.EncodeToString(m.Sum(nil))
}
func (c *Client) VerifyCallback(id, signature string, attempt ...string) bool {
	if c == nil || c.callbackSecret == "" {
		return false
	}
	want, err := hex.DecodeString(c.CallbackSignature(id, attempt...))
	got, gotErr := hex.DecodeString(signature)
	return err == nil && gotErr == nil && hmac.Equal(want, got)
}

func (c *Client) BatchSize() int {
	const defaultSize = 20
	raw := os.Getenv("JUDGE0_BATCH_SIZE")
	if raw == "" {
		return defaultSize
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultSize
	}
	return n
}

func (c *Client) CreateBatch(ctx context.Context, jobs []SubmissionRequest) ([]SubmissionResult, error) {
	if len(jobs) == 0 {
		return []SubmissionResult{}, nil
	}
	results := make([]SubmissionResult, len(jobs))
	if c.baseURL == "" {
		err := fmt.Errorf("%w: JUDGE0_URL is not set", ErrDispatchRejected)
		for i := range results {
			results[i].Error = err
		}
		return results, err
	}
	size := c.BatchSize()

	for start := 0; start < len(jobs); start += size {
		end := start + size
		if end > len(jobs) {
			end = len(jobs)
		}
		chunk := jobs[start:end]

		chunkResults, err := c.postBatch(ctx, chunk)
		if err != nil {
			for i := start; i < len(jobs); i++ {
				if results[i].Token == "" && results[i].Error == nil {
					failure := err
					if i >= end { // Later chunks never reached Judge0.
						failure = ErrDispatchNotSent
					}
					results[i] = SubmissionResult{Error: failure}
				}
			}
			return results, err
		}

		copy(results[start:end], chunkResults)
	}

	return results, nil
}

func (c *Client) GetSubmissions(ctx context.Context, tokens []string) ([]*SubmissionStatusResponse, error) {
	// Scoring polls only need stdout when verifying Judge0's blank-output defect.
	return c.getSubmissions(ctx, tokens, "token,time,memory,status,compile_output,stderr")
}

func (c *Client) GetSubmissionsWithOutput(ctx context.Context, tokens []string) ([]*SubmissionStatusResponse, error) {
	return c.getSubmissions(ctx, tokens, "token,time,memory,status,stdout,compile_output,stderr")
}

func (c *Client) GetSubmissionStdout(ctx context.Context, token string) (json.RawMessage, error) {
	results, err := c.getSubmissions(ctx, []string{token}, "token,status,stdout")
	if err != nil {
		return nil, err
	}
	if results[0] == nil || results[0].Status.ID != 4 || len(results[0].Stdout) == 0 {
		return nil, ErrInvalidResponse
	}
	return results[0].Stdout, nil
}

func (c *Client) getSubmissions(ctx context.Context, tokens []string, fields string) ([]*SubmissionStatusResponse, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("%w: JUDGE0_URL is not set", ErrUnavailable)
	}
	query := url.Values{"tokens": {strings.Join(tokens, ",")}, "base64_encoded": {"true"}, "fields": {fields}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/submissions/batch?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if c.authToken != "" {
		req.Header.Set("X-Auth-Token", c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, errors.Join(ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	var out struct {
		Submissions []*SubmissionStatusResponse `json:"submissions"`
	}
	// Keep the response bound proportional to the requested batch.
	if err = json.NewDecoder(io.LimitReader(resp.Body, int64(len(tokens))*maxResultBytesPerSubmission)).Decode(&out); err != nil {
		var networkErr net.Error
		if errors.As(err, &networkErr) || errors.Is(err, context.Canceled) {
			return nil, errors.Join(ErrUnavailable, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	if len(out.Submissions) != len(tokens) {
		return nil, ErrInvalidResponse
	}
	for i, result := range out.Submissions {
		if result != nil && (result.Token != tokens[i] || result.Status.ID < 1 || result.Status.ID > 14) {
			return nil, ErrInvalidResponse
		}
	}
	return out.Submissions, nil
}

func (c *Client) postBatch(ctx context.Context, jobs []SubmissionRequest) ([]SubmissionResult, error) {
	encoded := make([]SubmissionRequest, len(jobs))
	for i, job := range jobs {
		job.SourceCode = base64.StdEncoding.EncodeToString([]byte(job.SourceCode))
		job.Stdin = base64.StdEncoding.EncodeToString([]byte(job.Stdin))
		if job.ExpectedOutput != nil {
			output := base64.StdEncoding.EncodeToString([]byte(*job.ExpectedOutput))
			job.ExpectedOutput = &output
		}
		encoded[i] = job
	}
	payload, err := json.Marshal(map[string][]SubmissionRequest{
		"submissions": encoded,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/submissions/batch?base64_encoded=true", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Judge0 URL", ErrDispatchRejected)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("X-Auth-Token", c.authToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		var dialErr *net.OpError
		if errors.As(err, &dialErr) && dialErr.Op == "dial" {
			return nil, fmt.Errorf("%w: %w", ErrDispatchNotSent, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		failure := ErrUnavailable
		// Proxy 503s remain ambiguous.
		if resp.StatusCode == http.StatusServiceUnavailable && bytes.Equal(bytes.TrimSpace(body), []byte(`{"error":"queue is full"}`)) {
			failure = ErrQueueFull
		} else if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout {
			failure = ErrDispatchRejected
		}
		return nil, fmt.Errorf("%w: status %d: %s", failure, resp.StatusCode, string(body))
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	results := make([]SubmissionResult, len(jobs))
	for i := range jobs {
		if i >= len(entries) {
			results[i] = SubmissionResult{Error: ErrInvalidResponse}
			continue
		}
		var token batchTokenResponse
		if err := json.Unmarshal(entries[i], &token); err != nil {
			results[i] = SubmissionResult{Error: ErrInvalidResponse}
			continue
		}
		if token.Token == "" {
			var validation map[string]json.RawMessage
			_ = json.Unmarshal(entries[i], &validation)
			failure := ErrInvalidResponse
			for key, value := range validation {
				if key != "token" && len(value) > 0 && value[0] == '[' {
					failure = ErrDispatchRejected
				}
			}
			results[i] = SubmissionResult{Error: failure}
			continue
		}
		results[i] = SubmissionResult{Token: token.Token}
	}
	return results, nil
}
