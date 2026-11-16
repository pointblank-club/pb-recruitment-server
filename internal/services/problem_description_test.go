package services

import (
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/stores"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type descriptionProblemStore struct {
	*stores.ProblemStore
	updated *models.Problem
}

func (s *descriptionProblemStore) GetProblem(context.Context, string, string) (*dto.GetProblemStatementResponse, error) {
	return &dto.GetProblemStatementResponse{Description: "problems/contest/problem/description.json"}, nil
}

func (s *descriptionProblemStore) UpdateProblem(_ context.Context, problem *models.Problem) error {
	s.updated = problem
	return nil
}

func TestUpdateProblemDescriptionPresence(t *testing.T) {
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected S3 method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read uploaded description: %v", err)
		}
		uploaded = body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	for key, value := range map[string]string{
		"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "",
		"AWS_REGION": "us-east-1", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null", "AWS_CONFIG_FILE": "/dev/null",
		"AWS_EC2_METADATA_DISABLED": "true", "S3_SUBMISSIONS_BUCKET": "test", "AWS_ENDPOINT_URL_S3": server.URL,
	} {
		t.Setenv(key, value)
	}

	for _, tc := range []struct {
		name            string
		body            string
		wantUpload      bool
		wantDescription string
	}{
		{
			name:            "omitted preserves existing description",
			body:            `{"name":"edited","score":10,"type":"mcq","options":["a","b"],"answer":[0]}`,
			wantDescription: "problems/contest/problem/description.json",
		},
		{
			name:            "explicit empty clears description",
			body:            `{"name":"edited","score":10,"type":"mcq","description":"","options":["a","b"],"answer":[0]}`,
			wantUpload:      true,
			wantDescription: "problems/contest/problem/description.json",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uploaded = nil
			var req dto.CreateProblemRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatal(err)
			}
			problems := &descriptionProblemStore{}
			contest := problemContestTestStore{start: time.Now().Add(time.Hour).UnixMilli(), end: time.Now().Add(2 * time.Hour).UnixMilli()}
			service := NewContestService(&stores.Storage{Contests: contest, Problems: problems}, s3.NewS3Client())
			updated, err := service.UpdateProblem(context.Background(), "contest", "problem", &req)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Description != tc.wantDescription || problems.updated == nil || problems.updated.Description != tc.wantDescription {
				t.Fatalf("description not preserved: returned=%q stored=%+v", updated.Description, problems.updated)
			}
			if (uploaded != nil) != tc.wantUpload {
				t.Fatalf("uploaded body=%q, want upload %v", uploaded, tc.wantUpload)
			}
			if tc.wantUpload {
				var payload map[string]string
				if err := json.Unmarshal(uploaded, &payload); err != nil {
					t.Fatal(err)
				}
				if payload["description"] != "" {
					t.Fatalf("uploaded description=%q, want empty", payload["description"])
				}
			}
		})
	}
}
