package job_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/job"
)

var processDriverNumber atomic.Uint64

type finishErrorDriver struct {
	failQuery int
	calls     *int
}

type finishErrorConn struct{ finishErrorDriver }
type finishErrorTx struct{}
type finishAssetRows struct{ returned bool }

func (d finishErrorDriver) Open(string) (driver.Conn, error) {
	return &finishErrorConn{finishErrorDriver: d}, nil
}

func (*finishErrorConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unsupported")
}
func (*finishErrorConn) Close() error              { return nil }
func (*finishErrorConn) Begin() (driver.Tx, error) { return finishErrorTx{}, nil }
func (finishErrorTx) Commit() error                { return nil }
func (finishErrorTx) Rollback() error              { return nil }

func (c *finishErrorConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	*c.calls = *c.calls + 1
	if *c.calls == c.failQuery {
		return nil, errors.New("secret-driver-error")
	}
	return &finishAssetRows{}, nil
}

func (*finishAssetRows) Columns() []string {
	return []string{"storage_bucket", "storage_key", "live"}
}
func (*finishAssetRows) Close() error { return nil }
func (r *finishAssetRows) Next(dest []driver.Value) error {
	if r.returned {
		return io.EOF
	}
	r.returned = true
	dest[0], dest[1], dest[2] = "fixture-bucket", "fixture-key", true
	return nil
}

func TestProcessFinishPreservesDatabaseFailures(t *testing.T) {
	for _, failQuery := range []int{1, 2} {
		t.Run(fmt.Sprintf("query_%d", failQuery), func(t *testing.T) {
			calls := 0
			name := fmt.Sprintf("process-finish-error-%d", processDriverNumber.Add(1))
			sql.Register(name, finishErrorDriver{failQuery: failQuery, calls: &calls})
			db, err := sql.Open(name, "")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := job.SQLProcessStore{DB: db}
			asset := job.ProcessAsset{
				ID: "asset", Bucket: "fixture-bucket", Key: "fixture-key",
				Token: "fixture-token", OutputID: strings.Repeat("a", 32),
			}
			err = store.Finish(context.Background(), asset, job.ProcessOutcome{
				AssetStatus: "uploaded_unvalidated",
				Code:        "PROCESS_INTERRUPTED",
			})
			if !errors.Is(err, job.ErrProcessDatabase) || errors.Is(err, job.ErrProcessStale) {
				t.Fatalf("database error classified as stale/success: %v", err)
			}
			if strings.Contains(err.Error(), "secret-driver-error") || calls != failQuery {
				t.Fatalf("unexpected sanitization/query count: error=%v calls=%d", err, calls)
			}
		})
	}
}
