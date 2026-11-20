package preparation_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/preparation"
)

var deadlineDriverNumber atomic.Uint64

type deadlineDriver struct{ observed chan bool }
type deadlineConn struct{ observed chan bool }

func (d deadlineDriver) Open(string) (driver.Conn, error) {
	return deadlineConn{observed: d.observed}, nil
}
func (deadlineConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (deadlineConn) Close() error                        { return nil }
func (deadlineConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }
func (c deadlineConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	deadline, ok := ctx.Deadline()
	c.observed <- ok && time.Until(deadline) <= 5*time.Second && time.Until(deadline) > 0
	return nil, errors.New("fixture query failed")
}

// Capture contexts at the SQL driver boundary without contacting a database.
func TestOwnerReadsHaveBoundedDatabaseContexts(t *testing.T) {
	t.Parallel()
	observed := make(chan bool, 2)
	name := fmt.Sprintf("preparation-read-deadline-%d", deadlineDriverNumber.Add(1))
	sql.Register(name, deadlineDriver{observed: observed})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := preparation.SQLStore{DB: db}
	if _, err := store.GetStatus(context.Background(), "owner", "asset"); !errors.Is(err, preparation.ErrDatabase) {
		t.Fatal(err)
	}
	if !<-observed {
		t.Fatal("GetStatus query lacks five-second deadline")
	}
}
