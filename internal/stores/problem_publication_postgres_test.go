package stores_test

import (
	"app/internal/middleware"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type authoringObjects struct {
	mu                     sync.Mutex
	data                   map[string]string
	failSuffix             string
	entered, release       chan struct{}
	getEntered, getRelease chan struct{}
	getPath                string
}

func (o *authoringObjects) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		body, _ := io.ReadAll(r.Body)
		if o.entered != nil && strings.HasSuffix(r.URL.Path, "description.json") && strings.Contains(string(body), "metadata-only") {
			close(o.entered)
			<-o.release
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.failSuffix != "" && strings.HasSuffix(r.URL.Path, o.failSuffix) {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(403)
			io.WriteString(w, `<Error><Code>AccessDenied</Code></Error>`)
			return
		}
		if r.Header.Get("If-None-Match") == "*" {
			if _, ok := o.data[r.URL.Path]; ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(412)
				io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)
				return
			}
		}
		o.data[r.URL.Path] = string(body)
		return
	}
	if o.getEntered != nil && r.URL.Path == o.getPath {
		close(o.getEntered)
		<-o.getRelease
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	body, ok := o.data[r.URL.Path]
	if !ok {
		w.WriteHeader(404)
		return
	}
	io.WriteString(w, body)
}

func authoringFixture(t *testing.T) (*sql.DB, *authoringObjects, *services.ContestService, string, string, string) {
	t.Helper()
	db := postgresTestDB(t)
	_, contest, problem, _, _ := executionFixture(t, db, 0)
	oldKey := "problems/" + contest + "/" + problem + "/testcases.json"
	if _, err := db.Exec(`UPDATE contests SET start_time=$2 WHERE id=$1`, contest, time.Now().UnixMilli()+3600000); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE problems SET testcases=$2,description='old description' WHERE id=$1`, problem, oldKey); err != nil {
		t.Fatal(err)
	}
	o := &authoringObjects{data: map[string]string{"/test/" + oldKey: `[{"index":0,"input":"old-input","is_sample":true}]`, "/test/" + strings.TrimSuffix(oldKey, "testcases.json") + "answers.json": `["old-answer"]`}}
	server := httptest.NewServer(o)
	t.Cleanup(server.Close)
	for key, value := range map[string]string{"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "", "AWS_REGION": "us-east-1", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null", "AWS_CONFIG_FILE": "/dev/null", "AWS_EC2_METADATA_DISABLED": "true", "S3_SUBMISSIONS_BUCKET": "test", "AWS_ENDPOINT_URL_S3": server.URL} {
		t.Setenv(key, value)
	}
	return db, o, services.NewContestService(stores.NewStorage(db), s3.NewS3Client()), contest, problem, oldKey
}

func authoringRequest(name string, withCases bool) *dto.CreateProblemRequest {
	r := &dto.CreateProblemRequest{Name: name, Description: name, Score: 10, Type: models.Code, Testcases: []dto.CreateTestCaseRequest{}}
	if withCases {
		r.Testcases = []dto.CreateTestCaseRequest{{Input: "new-input", ExpectedOutput: "new-answer", IsSample: true}}
	}
	return r
}

func storedTestcaseKey(t *testing.T, db *sql.DB, problem string) string {
	t.Helper()
	var key string
	if err := db.QueryRow(`SELECT testcases FROM problems WHERE id=$1`, problem).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestProblemConcurrentMetadataEditPreservesVersionPostgres(t *testing.T) {
	db, o, cs, contest, problem, old := authoringFixture(t)
	ctx := context.Background()
	metadata := authoringRequest("metadata-only", false)
	if err := middleware.NewValidator().Validate(metadata); err != nil {
		t.Fatal("metadata-only request is not reachable", err)
	}
	o.entered, o.release = make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(o.release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() { _, err := cs.UpdateProblem(ctx, contest, problem, metadata); done <- err }()
	select {
	case <-o.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("metadata edit did not reach storage")
	}
	if _, err := cs.UpdateProblem(ctx, contest, problem, authoringRequest("testcase-edit", true)); err != nil {
		t.Fatal(err)
	}
	published := storedTestcaseKey(t, db, problem)
	if published == old {
		t.Fatal("testcase update failed to publish")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if current := storedTestcaseKey(t, db, problem); current != published {
		t.Fatalf("metadata edit reverted testcase version: current=%s published=%s", current, published)
	}
}

func TestProblemPublicationFailuresPreserveReferencesPostgres(t *testing.T) {
	for _, stage := range []string{"inputs", "answers", "database"} {
		t.Run(stage, func(t *testing.T) {
			db, o, cs, contest, problem, old := authoringFixture(t)
			ctx := context.Background()
			r := authoringRequest("publication", true)
			if stage == "inputs" {
				o.failSuffix = "testcases.json"
			}
			if stage == "answers" {
				o.failSuffix = "answers.json"
			}
			if stage == "database" {
				_, err := db.Exec(`CREATE FUNCTION review_reject_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.name='db-failure' THEN RAISE EXCEPTION 'review database failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER review_reject BEFORE UPDATE ON problems FOR EACH ROW EXECUTE FUNCTION review_reject_update()`)
				if err != nil {
					t.Fatal(err)
				}
				r.Name = "db-failure"
			}
			if _, err := cs.UpdateProblem(ctx, contest, problem, r); err == nil {
				t.Fatal("expected publication failure")
			}
			if key := storedTestcaseKey(t, db, problem); key != old {
				t.Fatal("failed update changed reference")
			}
			answers, err := cs.GetProblemAnswers(ctx, contest, problem)
			if err != nil || len(answers) != 1 || answers[0] != "old-answer" {
				t.Fatal(answers, err)
			}
			o.mu.Lock()
			o.failSuffix = ""
			o.mu.Unlock()
			if _, err := cs.UpdateProblem(ctx, contest, problem, authoringRequest("retry", true)); err != nil {
				t.Fatal(err)
			}
			if key := storedTestcaseKey(t, db, problem); key == old {
				t.Fatal("retry did not publish")
			}
			answers, err = cs.GetProblemAnswers(ctx, contest, problem)
			if err != nil || answers[0] != "new-answer" {
				t.Fatal(answers, err)
			}
			o.mu.Lock()
			oldAnswers := o.data["/test/"+strings.TrimSuffix(old, "testcases.json")+"answers.json"]
			o.mu.Unlock()
			if oldAnswers != `["old-answer"]` {
				t.Fatal("old version removed")
			}
		})
	}
}

func TestAdminProblemReadDuringEditKeepsVersionPairPostgres(t *testing.T) {
	db, o, cs, contest, problem, old := authoringFixture(t)
	ctx := context.Background()
	o.getEntered, o.getRelease, o.getPath = make(chan struct{}), make(chan struct{}), "/test/"+old
	done := make(chan *dto.GetProblemStatementResponse, 1)
	errs := make(chan error, 1)
	go func() { r, err := cs.GetContestProblem(ctx, contest, problem, true); done <- r; errs <- err }()
	select {
	case <-o.getEntered:
	case <-time.After(5 * time.Second):
		close(o.getRelease)
		t.Fatal("problem read did not reach storage")
	}
	_, err := cs.UpdateProblem(ctx, contest, problem, authoringRequest("new-version", true))
	close(o.getRelease)
	if err != nil {
		t.Fatal(err)
	}
	r := <-done
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if len(r.Testcases) != 1 || r.Testcases[0].Input != "old-input" || r.Testcases[0].ExpectedOutput != "old-answer" {
		t.Fatal(r.Testcases)
	}
	if storedTestcaseKey(t, db, problem) == old {
		t.Fatal("no new version published")
	}
}
