package services

import (
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/stores"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type submissionTestStore struct {
	*stores.SubmissionStore
	markFailed func(context.Context, string) error
	judgeMCQ   func(context.Context, string) error
}

func (submissionTestStore) CreateSubmission(context.Context, *models.Submission) (string, error) {
	return "submission", nil
}

func (s submissionTestStore) MarkFailed(ctx context.Context, id string) error {
	return s.markFailed(ctx, id)
}

func (s submissionTestStore) JudgeMCQ(ctx context.Context, id string) error {
	if s.judgeMCQ == nil {
		return nil
	}
	return s.judgeMCQ(ctx, id)
}

type problemTestStore struct{ *stores.ProblemStore }

func (problemTestStore) GetProblem(context.Context, string, string) (*dto.GetProblemStatementResponse, error) {
	return &dto.GetProblemStatementResponse{TimeLimit: 1000, MemoryLimit: 256}, nil
}

type executionTestStore struct {
	*stores.ExecutionStore
	saveTokens  func(context.Context, map[string]string) error
	markFailed  func(context.Context, []string) error
	insertBatch func(context.Context) error
}

func (s executionTestStore) InsertBatch(ctx context.Context, _ string, _ []int) ([]models.Execution, error) {
	if s.insertBatch != nil {
		if err := s.insertBatch(ctx); err != nil {
			return nil, err
		}
	}
	return []models.Execution{{ID: "execution"}}, nil
}

func (s executionTestStore) SaveTokens(ctx context.Context, tokens map[string]string) error {
	return s.saveTokens(ctx, tokens)
}

func (s executionTestStore) MarkFailed(ctx context.Context, ids []string) error {
	return s.markFailed(ctx, ids)
}

func TestCreateSubmissionFailureBookkeeping(t *testing.T) {
	writeErr := errors.New("failure status write failed")
	for _, tc := range []struct {
		name        string
		timeoutSave bool
		markErr     error
		failUpload  bool
		failInsert  bool
	}{
		{name: "token save timeout", timeoutSave: true},
		{name: "failure status write error", markErr: writeErr},
		{name: "code upload failure", failUpload: true},
		{name: "execution insertion failure", failInsert: true},
		{name: "preparation cleanup failure", failInsert: true, markErr: writeErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					if tc.failUpload {
						w.Header().Set("Content-Type", "application/xml")
						w.WriteHeader(http.StatusForbidden)
						w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
					} else {
						w.WriteHeader(http.StatusOK)
					}
				} else if strings.HasSuffix(r.URL.Path, "testcases.json") {
					w.Write([]byte(`[{"index":0,"input":"1"}]`))
				} else {
					w.Write([]byte(`["1"]`))
				}
			}))
			defer objects.Close()
			for key, value := range map[string]string{
				"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "",
				"AWS_REGION": "us-east-1", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null",
				"AWS_CONFIG_FILE": "/dev/null", "AWS_EC2_METADATA_DISABLED": "true",
				"S3_SUBMISSIONS_BUCKET": "test", "AWS_ENDPOINT_URL_S3": objects.URL,
			} {
				t.Setenv(key, value)
			}
			judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.failUpload || tc.failInsert {
					t.Error("dispatch must not run after preparation failure")
				}
				if tc.timeoutSave {
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte(`[{"token":"token"}]`))
				} else {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer judge.Close()
			t.Setenv("JUDGE0_URL", judge.URL)
			t.Setenv("JUDGE0_AUTH_TOKEN", "")
			t.Setenv("JUDGE0_CALLBACK_BASE_URL", "")
			t.Setenv("JUDGE0_TIMEOUT_MS", "1000")
			t.Setenv("JUDGE0_BATCH_SIZE", "20")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			marked := false
			checkContext := func(ctx context.Context) error {
				if err := ctx.Err(); err != nil {
					t.Errorf("failure bookkeeping received expired context: %v", err)
					return err
				}
				if _, bounded := ctx.Deadline(); !bounded {
					t.Error("failure bookkeeping needs a bounded context")
				}
				return nil
			}
			store := &stores.Storage{
				Submissions: submissionTestStore{markFailed: func(ctx context.Context, id string) error {
					marked = true
					if !tc.failUpload && !tc.failInsert {
						t.Error("unexpected parent failure write")
					}
					if id != "submission" {
						t.Errorf("failed submission ID = %q", id)
					}
					if err := checkContext(ctx); err != nil {
						return err
					}
					return tc.markErr
				}}, Problems: problemTestStore{},
				Executions: executionTestStore{
					insertBatch: func(ctx context.Context) error {
						if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
							t.Error("submission preparation must be bounded to five seconds")
						}
						if tc.failInsert {
							cancel()
							return context.Canceled
						}
						return nil
					},
					saveTokens: func(ctx context.Context, tokens map[string]string) error {
						if !tc.timeoutSave || tokens["execution"] != "token" {
							t.Fatal("unexpected token save")
						}
						cancel()
						<-ctx.Done()
						return ctx.Err()
					},
					markFailed: func(ctx context.Context, ids []string) error {
						marked = true
						if err := checkContext(ctx); err != nil {
							return err
						}
						if len(ids) != 1 || ids[0] != "execution" {
							t.Errorf("failed execution IDs = %v", ids)
						}
						return tc.markErr
					},
				},
			}
			service := NewSubmissionService(store, s3.NewS3Client(), judge0.NewClient())
			id, err := service.CreateSubmission(ctx, "user", models.Code, &dto.SubmitSubmissionRequest{
				ContestID: "contest", ProblemID: "problem", Language: "cpp",
				Code: base64.StdEncoding.EncodeToString([]byte("int main(){}")),
			})
			if !marked {
				t.Error("failure bookkeeping was not called")
			}
			if tc.failUpload || tc.failInsert {
				if err == nil {
					t.Error("preparation failure must return an error")
				}
				if tc.failInsert && !errors.Is(err, context.Canceled) {
					t.Errorf("lost insertion error: %v", err)
				}
			}
			if tc.markErr != nil || (!tc.failUpload && !tc.failInsert) {
				if !errors.Is(err, tc.markErr) {
					t.Errorf("CreateSubmission error = %v, want %v", err, tc.markErr)
				}
			}
			if err == nil && id != "submission" {
				t.Errorf("submission ID = %q", id)
			}
		})
	}
}
