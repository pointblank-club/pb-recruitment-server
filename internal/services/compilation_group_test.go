package services

import (
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/stores"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type groupedExecutionStore struct {
	*stores.ExecutionStore
	jobs []judge0.SubmissionRequest
}

func (s *groupedExecutionStore) InsertBatch(_ context.Context, _ string, indexes []int, snapshots ...[]judge0.SubmissionRequest) ([]models.Execution, error) {
	s.jobs = append([]judge0.SubmissionRequest(nil), snapshots[0]...)
	executions := make([]models.Execution, len(indexes))
	for i := range indexes {
		executions[i].ID = fmt.Sprintf("execution-%d", i)
	}
	return executions, nil
}
func (s *groupedExecutionStore) SaveAttemptToken(context.Context, string, string, string) error {
	return nil
}

func TestCompilationGroupsPersistedAndDispatched(t *testing.T) {
	for _, tc := range []struct {
		name, language, enabled string
		grouped                 bool
	}{{"disabled", "cpp", "false", false}, {"cpp", "cpp", "true", true}, {"java", "java", "true", true}, {"python unchanged", "python", "true", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("JUDGE0_COMPILE_ONCE", tc.enabled)
			t.Setenv("JUDGE0_CALLBACK_BASE_URL", "")
			t.Setenv("JUDGE0_CALLBACK_SECRET", "")
			objectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "testcases.json"):
					fmt.Fprint(w, `[{"index":0,"input":"1 2"},{"index":1,"input":"3 4"}]`)
				case strings.HasSuffix(r.URL.Path, "answers.json"):
					fmt.Fprint(w, `["3","7"]`)
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer objectServer.Close()
			for key, value := range map[string]string{"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_REGION": "us-east-1", "AWS_ENDPOINT_URL_S3": objectServer.URL, "S3_SUBMISSIONS_BUCKET": "test"} {
				t.Setenv(key, value)
			}
			var sent []judge0.SubmissionRequest
			judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Submissions []judge0.SubmissionRequest `json:"submissions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				sent = body.Submissions
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `[{"token":"first"},{"token":"second"}]`)
			}))
			defer judge.Close()
			t.Setenv("JUDGE0_URL", judge.URL)
			parent := "a2e19397-8d16-43fb-b5ce-481ee63fc4f4"
			executions := &groupedExecutionStore{}
			storage := &stores.Storage{Submissions: submissionTestStore{createSubmission: func(context.Context, *models.Submission) (string, error) { return parent, nil }}, Problems: problemTestStore{}, Executions: executions}
			id, err := NewSubmissionService(storage, s3.NewS3Client(), judge0.NewClient()).CreateSubmission(context.Background(), "synthetic", models.Code, &dto.SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: models.Code, Language: tc.language, Code: "source"})
			if err != nil || id != parent {
				t.Fatalf("submission: %s %v", id, err)
			}
			want := ""
			if tc.grouped {
				want = parent
			}
			if len(executions.jobs) != 2 || len(sent) != 2 {
				t.Fatalf("lost per-case jobs: snapshots=%d sent=%d", len(executions.jobs), len(sent))
			}
			for _, jobs := range [][]judge0.SubmissionRequest{executions.jobs, sent} {
				for _, job := range jobs {
					if job.CompilationGroup != want {
						t.Errorf("group=%q want=%q", job.CompilationGroup, want)
					}
				}
			}
		})
	}
}
