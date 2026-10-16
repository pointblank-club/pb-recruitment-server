package judge0

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBatchRejections(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   []error
	}{
		{"queue full", 503, `{"error":"queue is full"}`, []error{ErrDispatchRejected, ErrDispatchRejected}},
		{"invalid request", 422, `[{"memory_limit":["exceeds maximum"]}]`, []error{ErrDispatchRejected, ErrDispatchRejected}},
		{"mixed validation", 201, `[{"token":"accepted"},{"memory_limit":["exceeds maximum"]}]`, []error{nil, ErrDispatchRejected}},
		{"empty entry", 201, `[{"token":"accepted"},{}]`, []error{nil, ErrInvalidResponse}},
		{"missing entry", 201, `[{"token":"accepted"}]`, []error{nil, ErrInvalidResponse}},
		{"internal error", 500, `{}`, []error{ErrUnavailable, ErrUnavailable}},
		{"bad gateway", 502, `{}`, []error{ErrUnavailable, ErrUnavailable}},
		{"gateway timeout", 504, `{}`, []error{ErrUnavailable, ErrUnavailable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			t.Setenv("JUDGE0_URL", server.URL)
			t.Setenv("JUDGE0_BATCH_SIZE", "20")
			results, _ := NewClient().CreateBatch(context.Background(), make([]SubmissionRequest, 2))
			if len(results) != len(tc.want) {
				t.Fatalf("results = %+v", results)
			}
			for i, want := range tc.want {
				if !errors.Is(results[i].Error, want) {
					t.Errorf("result %d error = %v, want %v", i, results[i].Error, want)
				}
				if want == nil && results[i].Token != "accepted" {
					t.Errorf("accepted token lost: %+v", results[i])
				}
			}
		})
	}
}

func TestURLTrailingSlashes(t *testing.T) {
	for _, suffix := range []string{"", "/", "///"} {
		t.Run(suffix, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "//") {
					t.Errorf("double slash in Judge0 request: %s", r.URL.Path)
				}
				if r.Method == http.MethodPost {
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte(`[{"token":"accepted"}]`))
				} else {
					w.Write([]byte(`{"submissions":[{"token":"accepted","status":{"id":3}}]}`))
				}
			}))
			defer server.Close()
			t.Setenv("JUDGE0_URL", server.URL+suffix)
			t.Setenv("JUDGE0_CALLBACK_BASE_URL", "https://example.test"+suffix)
			t.Setenv("JUDGE0_CALLBACK_SECRET", "test-secret")
			client := NewClient()
			id := "e032ee69-cfd1-4350-b083-cc6e9822e9ec"
			callback, err := url.Parse(client.CallbackURL(id))
			if err != nil {
				t.Fatal(err)
			}
			if callback.Path != "/internal/judge0/callback/"+id || !client.VerifyCallback(id, callback.Query().Get("sig")) {
				t.Fatalf("invalid callback path or signature: %s", callback.Path)
			}
			if _, err := client.CreateBatch(context.Background(), []SubmissionRequest{{}}); err != nil {
				t.Fatal(err)
			}
			if _, err := client.GetSubmissions(context.Background(), []string{"accepted"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBatchBodyReadFailureIsAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`[{"token":"`))
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	t.Setenv("JUDGE0_AUTH_TOKEN", "")
	results, err := NewClient().CreateBatch(context.Background(), []SubmissionRequest{{LanguageID: 54}})
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("body-read error = %v", err)
	}
	if len(results) != 1 || results[0].Token != "" || !errors.Is(results[0].Error, ErrUnavailable) {
		t.Fatalf("ambiguous result = %+v", results)
	}
}

func TestBatchResultValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		invalid    bool
	}{
		{"mixed found and missing", `{"submissions":[{"token":"a","status":{"id":3}},null]}`, false},
		{"missing entries", `{"submissions":[]}`, true},
		{"wrong token", `{"submissions":[{"token":"other","status":{"id":3}},null]}`, true},
		{"invalid status", `{"submissions":[{"token":"a","status":{"id":0}},null]}`, true},
		{"malformed body", `{`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/submissions/batch" || r.URL.Query().Get("tokens") != "a,b" || r.Header.Get("X-Auth-Token") != "test-token" {
					t.Error("invalid batch request or authentication")
				}
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			t.Setenv("JUDGE0_URL", server.URL)
			t.Setenv("JUDGE0_AUTH_TOKEN", "test-token")
			results, err := NewClient().GetSubmissions(context.Background(), []string{"a", "b"})
			if tc.invalid {
				if !errors.Is(err, ErrInvalidResponse) {
					t.Fatalf("invalid response error = %v", err)
				}
			} else if err != nil || len(results) != 2 || results[0] == nil || results[1] != nil {
				t.Fatalf("results = %+v, err = %v", results, err)
			}
		})
	}
}

func TestGetSubmissionsRequestsBase64(t *testing.T) {
	// Judge0 rejects the entire batch GET with 400 if any submission
	// has non-UTF-8 stderr and base64_encoded is false.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("base64_encoded"); got != "true" {
			t.Errorf("base64_encoded = %q, want %q", got, "true")
		}
		w.Write([]byte(`{"submissions":[{"token":"a","status":{"id":3}}]}`))
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	t.Setenv("JUDGE0_AUTH_TOKEN", "")
	if _, err := NewClient().GetSubmissions(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchTimeout(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", 10 * time.Second},
		{"1000", time.Second},
		{"30000", 10 * time.Second},
		{"0", 10 * time.Second},
		{"-1", 10 * time.Second},
		{"invalid", 10 * time.Second},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("JUDGE0_TIMEOUT_MS", tc.value)
			client := NewClient()
			if client.Timeout() != tc.want || client.httpClient.Timeout != tc.want {
				t.Fatalf("timeouts = %v/%v, want %v", client.Timeout(), client.httpClient.Timeout, tc.want)
			}
		})
	}
	if timeout := (*Client)(nil).Timeout(); timeout != 10*time.Second {
		t.Fatalf("fallback timeout = %v", timeout)
	}
}

func TestBase64AndPartialBatchMapping(t *testing.T) {
	for _, failAt := range []int32{0, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := requests.Add(1)
				if r.URL.Query().Get("base64_encoded") != "true" {
					t.Error("base64 contract disabled")
				}
				var payload struct {
					Submissions []SubmissionRequest `json:"submissions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				for i, job := range payload.Submissions {
					for _, value := range []struct{ got, want string }{{job.SourceCode, "print('नमस्ते')\n"}, {job.Stdin, ""}, {*job.ExpectedOutput, "नमस्ते\n"}} {
						decoded, err := base64.StdEncoding.DecodeString(value.got)
						if err != nil || string(decoded) != value.want {
							t.Errorf("job %d encoded=%q want=%q error=%v", i, decoded, value.want, err)
						}
					}
				}
				if call == failAt {
					w.WriteHeader(504)
					return
				}
				w.WriteHeader(201)
				responses := make([]map[string]string, len(payload.Submissions))
				for i := range responses {
					responses[i] = map[string]string{"token": fmt.Sprint((int(call)-1)*2 + i)}
				}
				json.NewEncoder(w).Encode(responses)
			}))
			defer server.Close()
			t.Setenv("JUDGE0_URL", server.URL)
			t.Setenv("JUDGE0_BATCH_SIZE", "2")
			output := "नमस्ते\n"
			jobs := make([]SubmissionRequest, 5)
			for i := range jobs {
				jobs[i] = SubmissionRequest{SourceCode: "print('नमस्ते')\n", Stdin: "", ExpectedOutput: &output}
			}
			results, err := NewClient().CreateBatch(context.Background(), jobs)
			if failAt == 0 {
				if err != nil {
					t.Fatal(err)
				}
				for i, r := range results {
					if r.Token != fmt.Sprint(i) {
						t.Fatalf("result %d=%+v", i, r)
					}
				}
			} else {
				if !errors.Is(err, ErrUnavailable) || results[0].Token != "0" || results[1].Token != "1" || !errors.Is(results[2].Error, ErrUnavailable) || !errors.Is(results[3].Error, ErrUnavailable) || !errors.Is(results[4].Error, ErrDispatchNotSent) {
					t.Fatalf("partial results=%+v, %v", results, err)
				}
			}
			if jobs[0].SourceCode != "print('नमस्ते')\n" || *jobs[0].ExpectedOutput != output {
				t.Fatal("client mutated caller inputs")
			}
		})
	}
}
func TestAcceptedDispatchTimeoutIsAmbiguous(t *testing.T) {
	var accepted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepted.Store(true)
		time.Sleep(60 * time.Millisecond)
		w.WriteHeader(201)
		w.Write([]byte(`[{"token":"already-created"}]`))
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	t.Setenv("JUDGE0_TIMEOUT_MS", "20")
	results, err := NewClient().CreateBatch(context.Background(), []SubmissionRequest{{SourceCode: "print(1)"}})
	if !accepted.Load() || !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrDispatchRejected) || len(results) != 1 || results[0].Token != "" {
		t.Fatalf("accepted=%v result=%+v err=%v", accepted.Load(), results, err)
	}
}
func TestCallbackAttemptAuthentication(t *testing.T) {
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", "https://callback.test")
	t.Setenv("JUDGE0_CALLBACK_SECRET", "secret")
	c := NewClient()
	id := "e032ee69-cfd1-4350-b083-cc6e9822e9ec"
	u, _ := url.Parse(c.CallbackURL(id, "attempt"))
	if u.Query().Get("attempt") != "attempt" || !c.VerifyCallback(id, u.Query().Get("sig"), "attempt") || c.VerifyCallback(id, u.Query().Get("sig"), "other") || c.VerifyCallback(id, u.Query().Get("sig")) {
		t.Fatal("attempt signature not fenced")
	}
}
