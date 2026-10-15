package services

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/stores"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func objectTestClient(t *testing.T, handler http.HandlerFunc) *s3.S3 {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	for key, value := range map[string]string{"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "", "AWS_REGION": "us-east-1", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null", "AWS_CONFIG_FILE": "/dev/null", "AWS_EC2_METADATA_DISABLED": "true", "S3_SUBMISSIONS_BUCKET": "test", "AWS_ENDPOINT_URL_S3": server.URL} {
		t.Setenv(key, value)
	}
	return s3.NewS3Client()
}
func TestTestcaseCacheConcurrentStorageLoads(t *testing.T) {
	var reads atomic.Int32
	storage := objectTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		time.Sleep(5 * time.Millisecond)
		if strings.HasSuffix(r.URL.Path, "testcases.json") {
			io.WriteString(w, `[{"index":0,"input":""},{"index":1,"input":"नमस्ते\n"}]`)
		} else {
			io.WriteString(w, `["empty\n","नमस्ते\n"]`)
		}
	})
	service := NewSubmissionService(nil, storage, nil)
	var wg sync.WaitGroup
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inputs, outputs, err := service.loadTestcases(context.Background(), "c", "p", "")
			if err != nil || len(inputs) != 2 || inputs[0] != "" || inputs[1] != "नमस्ते\n" || outputs[0] != "empty\n" {
				t.Errorf("cases=%v outputs=%v err=%v", inputs, outputs, err)
			}
		}()
	}
	wg.Wait()
	if reads.Load() != 2 {
		t.Fatalf("unchanged objects fetched %d times", reads.Load())
	}
}
func TestTestcaseCacheBoundsAndFailures(t *testing.T) {
	var cache testcaseCache
	ctx := context.Background()
	calls := 0
	fetch := func(context.Context) ([]string, []string, error) {
		calls++
		if calls == 1 {
			return nil, nil, common.ErrNoTestcases
		}
		return []string{"input"}, []string{"output"}, nil
	}
	if _, _, err := cache.load(ctx, "failure", fetch); !errors.Is(err, common.ErrNoTestcases) {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err := cache.load(ctx, "failure", fetch); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("cached failure or missed cache", calls)
	}
	for i := range 100 {
		if _, _, err := cache.load(ctx, fmt.Sprint(i), fetch); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != 64 || cache.bytes > testcaseCacheBytes {
		t.Fatalf("unbounded cache: %d %d", len(cache.entries), cache.bytes)
	}
	large := func(context.Context) ([]string, []string, error) {
		return []string{strings.Repeat("x", testcaseCacheBytes)}, []string{""}, nil
	}
	if _, _, err := cache.load(ctx, "large", large); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.entries["large"]; ok {
		t.Fatal("oversized object cached")
	}
	cache.mu.Lock()
	entry := cache.entries["99"]
	entry.loaded = time.Now().Add(-6 * time.Minute)
	cache.entries["99"] = entry
	cache.mu.Unlock()
	before := calls
	cache.load(ctx, "99", fetch)
	if calls != before+1 {
		t.Fatal("TTL did not expire")
	}
}

type versionProblemStore struct {
	*stores.ProblemStore
	key string
}

func (s *versionProblemStore) GetProblem(context.Context, string, string) (*dto.GetProblemStatementResponse, error) {
	return &dto.GetProblemStatementResponse{Type: models.Code, TestcasesKey: s.key}, nil
}
func (s *versionProblemStore) UpdateProblem(_ context.Context, p *models.Problem) error {
	s.key = p.Testcases
	return nil
}
func TestProblemEditVersionsCacheAcrossReplicas(t *testing.T) {
	objects := map[string]string{"/test/problems/c/p/testcases.json": `[{"index":0,"input":"old","is_sample":true},{"index":1,"input":"hidden"}]`, "/test/problems/c/p/answers.json": `["old answer","hidden answer"]`}
	var mu sync.Mutex
	objectClient := objectTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut {
			raw, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = string(raw)
			return
		}
		raw, ok := objects[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		io.WriteString(w, raw)
	})
	problemStore := &versionProblemStore{key: "problems/c/p/testcases.json"}
	storage := &stores.Storage{Problems: problemStore, Contests: problemContestTestStore{start: time.Now().UnixMilli() + 60000}}
	replicas := []*SubmissionService{NewSubmissionService(storage, objectClient, nil), NewSubmissionService(storage, objectClient, nil)}
	for _, ss := range replicas {
		if _, _, err := ss.loadTestcases(context.Background(), "c", "p", problemStore.key); err != nil {
			t.Fatal(err)
		}
	}
	oldKey := problemStore.key
	contestService := NewContestService(storage, objectClient)
	_, err := contestService.UpdateProblem(context.Background(), "c", "p", &dto.CreateProblemRequest{Name: "edited", Type: models.Code, Description: "new", Testcases: []dto.CreateTestCaseRequest{{Input: "new", ExpectedOutput: "new answer", IsSample: true}, {Input: "secret", ExpectedOutput: "secret answer"}}})
	if err != nil {
		t.Fatal(err)
	}
	if oldKey == problemStore.key {
		t.Fatal("testcase version did not change")
	}
	for _, ss := range replicas {
		inputs, outputs, err := ss.loadTestcases(context.Background(), "c", "p", problemStore.key)
		if err != nil || inputs[0] != "new" || outputs[0] != "new answer" {
			t.Fatal(inputs, outputs, err)
		}
	}
	statement, err := contestService.GetContestProblem(context.Background(), "c", "p", false)
	if err != nil || len(statement.Testcases) != 1 || statement.Testcases[0].ExpectedOutput != "new answer" {
		t.Fatal(statement, err)
	}
	answers, err := contestService.GetProblemAnswers(context.Background(), "c", "p")
	if err != nil || answers[1] != "secret answer" {
		t.Fatal(answers, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if objects["/test/"+testcaseAnswersKey(oldKey)] != `["old answer","hidden answer"]` {
		t.Fatal("old testcase version overwritten")
	}
}
