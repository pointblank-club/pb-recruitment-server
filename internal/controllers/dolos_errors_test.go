package controllers

import (
	"app/internal/common"
	"app/internal/models/dto"
	"app/internal/services"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
	"github.com/labstack/echo/v4"
)

type dolosFailingDB struct{ err error }

func (d dolosFailingDB) Connect(context.Context) (driver.Conn, error) {
	return dolosFailingConn{err: d.err}, nil
}
func (dolosFailingDB) Driver() driver.Driver { return dolosFailingDriver{} }

type dolosFailingDriver struct{}

func (dolosFailingDriver) Open(string) (driver.Conn, error) { return dolosFailingConn{}, nil }

type dolosFailingConn struct{ err error }

func (dolosFailingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (dolosFailingConn) Close() error              { return nil }
func (dolosFailingConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (d dolosFailingConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, d.err
}

func TestDolosPairsUnavailableAndUnexpectedError(t *testing.T) {
	for _, tc := range []struct {
		name, base      string
		status, reports int
		cause           error
		value           string
	}{
		{"disabled", "", 503, 0, errors.New("synthetic database failure"), ""},
		{"database failure", "http://127.0.0.1:1", 500, 1, errors.New("synthetic database failure"), "synthetic database failure"},
		{"provider error", "http://127.0.0.1:1", 500, 1, &url.Error{Op: "Get", URL: "https://provider.test/reports/private-report-id/data/pairs.csv", Err: context.DeadlineExceeded}, "context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOLOS_API_URL", tc.base)
			db := sql.OpenDB(dolosFailingDB{err: tc.cause})
			defer db.Close()
			var events []*sentry.Event
			client, err := sentry.NewClient(sentry.ClientOptions{Dsn: "http://public@127.0.0.1:1/1", DisableMetrics: true,
				BeforeSend: func(e *sentry.Event, _ *sentry.EventHint) *sentry.Event { events = append(events, e); return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			e := echo.New()
			w := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest("GET", "/synthetic", nil), w)
			sentryecho.SetHubOnContext(c, sentry.NewHub(client, sentry.NewScope()))
			controller := NewContestController(nil, services.NewDolosService(db, nil))
			if err := controller.GetDolosPairs(c); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || len(events) != tc.reports {
				t.Fatalf("status=%d reports=%d", w.Code, len(events))
			}
			if len(events) > 0 && (len(events[0].Exception) == 0 || events[0].Exception[0].Value != tc.value) {
				t.Fatal("underlying cause missing", events)
			}
			encoded, _ := json.Marshal(events)
			if strings.Contains(string(encoded), "private-report-id") {
				t.Fatal("report access URL leaked to Sentry")
			}
		})
	}
}

func TestDolosReviewUnavailable(t *testing.T) {
	t.Setenv("DOLOS_API_URL", "http://127.0.0.1:1")
	db := sql.OpenDB(dolosFailingDB{err: common.ErrDolosUnavailable})
	defer db.Close()
	e := echo.New()
	w := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest("PATCH", "/synthetic", nil), w)
	c.Set(common.VALIDATED_REQUEST_BODY, &dto.PlagiarismPairReviewRequest{})
	controller := NewContestController(nil, services.NewDolosService(db, nil))
	if err := controller.ReviewDolosPair(c); err != nil {
		t.Fatal(err)
	}
	if w.Code != 503 {
		t.Fatalf("status=%d", w.Code)
	}
}
