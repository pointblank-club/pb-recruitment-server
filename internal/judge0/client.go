package judge0

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/gommon/log"
)

var ErrDispatchRejected = fmt.Errorf("Judge0 explicitly rejected dispatch")

type Client struct {
	baseURL        string
	authToken      string
	callbackBase   string
	callbackSecret string
	httpClient     *http.Client
}

// Dispatch leaves ten seconds for DB bookkeeping within the browser's thirty-second timeout.
const maxDispatchTimeout = 10 * time.Second

func NewClient() *Client {
	callbackBase, callbackSecret := os.Getenv("JUDGE0_CALLBACK_BASE_URL"), os.Getenv("JUDGE0_CALLBACK_SECRET")
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
		baseURL:        os.Getenv("JUDGE0_URL"),
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

func (c *Client) CallbackURL(executionID string) string {
	if c.callbackBase == "" || c.callbackSecret == "" {
		return ""
	}
	sig := c.CallbackSignature(executionID)
	return fmt.Sprintf("%s/internal/judge0/callback/%s?sig=%s", c.callbackBase, executionID, sig)
}

func (c *Client) CallbacksEnabled() bool {
	return c != nil && c.callbackBase != "" && c.callbackSecret != ""
}
func (c *Client) CallbackSignature(id string) string {
	if parsed, err := uuid.Parse(id); err == nil {
		id = parsed.String()
	}
	m := hmac.New(sha256.New, []byte(c.callbackSecret))
	_, _ = m.Write([]byte(id))
	return hex.EncodeToString(m.Sum(nil))
}
func (c *Client) VerifyCallback(id, signature string) bool {
	if c == nil || c.callbackSecret == "" {
		return false
	}
	want, err := hex.DecodeString(c.CallbackSignature(id))
	got, gotErr := hex.DecodeString(signature)
	return err == nil && gotErr == nil && hmac.Equal(want, got)
}

func (c *Client) batchSize() int {
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
	if c.baseURL == "" {
		return nil, fmt.Errorf("%w: JUDGE0_URL is not set", ErrUnavailable)
	}
	if len(jobs) == 0 {
		return []SubmissionResult{}, nil
	}

	results := make([]SubmissionResult, len(jobs))
	size := c.batchSize()

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
					results[i] = SubmissionResult{Error: err}
				}
			}
			return results, err
		}

		copy(results[start:end], chunkResults)
	}

	return results, nil
}

func (c *Client) GetSubmission(ctx context.Context, token string) (*SubmissionStatusResponse, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("%w: JUDGE0_URL is not set", ErrUnavailable)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/submissions/"+token+"?base64_encoded=false&fields=token,time,memory,status", nil)
	if err != nil {
		return nil, err
	}
	if c.authToken != "" {
		req.Header.Set("X-Auth-Token", c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, ErrSubmissionNotFound
		}
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	var out SubmissionStatusResponse
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	return &out, nil
}

func (c *Client) postBatch(ctx context.Context, jobs []SubmissionRequest) ([]SubmissionResult, error) {
	payload, err := json.Marshal(map[string][]SubmissionRequest{
		"submissions": jobs,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/submissions/batch?base64_encoded=false", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("X-Auth-Token", c.authToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d: %s", ErrDispatchRejected, resp.StatusCode, string(body))
	}

	var tokens []batchTokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	results := make([]SubmissionResult, len(jobs))
	for i := range jobs {
		if i >= len(tokens) || tokens[i].Token == "" {
			results[i] = SubmissionResult{Error: ErrInvalidResponse}
			continue
		}
		results[i] = SubmissionResult{Token: tokens[i].Token}
	}
	return results, nil
}
