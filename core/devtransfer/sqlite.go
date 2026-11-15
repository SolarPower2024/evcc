package devtransfer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlite3 "modernc.org/sqlite"
	sqlite3lib "modernc.org/sqlite/lib"
)

// slot is the length of a metrics slot
const slot = 15 * time.Minute

// openFile opens a database file of its own connection pool, waiting 5 s on a lock
func openFile(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
}

// queryer is a *sql.DB or *sql.Conn
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// isBusy reports a locked database
func isBusy(err error) bool {
	var serr *sqlite3.Error
	if errors.As(err, &serr) {
		code := serr.Code() & 0xff
		return code == sqlite3lib.SQLITE_BUSY || code == sqlite3lib.SQLITE_LOCKED
	}
	return false
}

// wrapBusy turns a lock error into ErrLocked
func wrapBusy(err error) error {
	if err != nil && isBusy(err) {
		return fmt.Errorf("%w: %v", ErrLocked, err)
	}
	return err
}

// backupFile copies the database in path, with the content of its log, to
// target through sqlite's backup function
func backupFile(ctx context.Context, path, target string) error {
	sdb, err := openFile(path)
	if err != nil {
		return err
	}
	defer sdb.Close()

	conn, err := sdb.Conn(ctx)
	if err != nil {
		return wrapBusy(err)
	}
	defer conn.Close()

	return wrapBusy(conn.Raw(func(driverConn any) error {
		b, ok := driverConn.(interface {
			NewBackup(string) (*sqlite3.Backup, error)
		})
		if !ok {
			return errors.New("invalid db type")
		}

		bck, err := b.NewBackup(target)
		if err != nil {
			return err
		}

		if _, err := bck.Step(-1); err != nil {
			return err
		}

		return bck.Finish()
	}))
}

// toTime reads a time from a column value
func toTime(v any) (time.Time, bool) {
	switch v := v.(type) {
	case time.Time:
		return v, true
	case string:
		for _, layout := range []string{
			"2006-01-02 15:04:05.999999999-07:00",
			"2006-01-02T15:04:05.999999999Z07:00",
			"2006-01-02 15:04:05.999999999",
		} {
			if t, err := time.Parse(layout, strings.TrimSuffix(v, " UTC")); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// tableOf prefixes a table with its schema
func tableOf(schema, table string) string {
	if schema == "" {
		return table
	}
	return schema + "." + table
}

// sessionTimes returns loadpoint and created of every charging session in schema
func sessionTimes(ctx context.Context, q queryer, schema string) (map[int64]sessionKey, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, loadpoint, created FROM "+tableOf(schema, "sessions"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[int64]sessionKey)
	for rows.Next() {
		var (
			id      int64
			lp      sql.NullString
			created any
		)
		if err := rows.Scan(&id, &lp, &created); err != nil {
			return nil, err
		}
		t, ok := toTime(created)
		if !ok {
			continue
		}
		res[id] = sessionKey{lp.String, t.UnixNano()}
	}

	return res, rows.Err()
}

// sessionKey identifies a charging session
type sessionKey struct {
	loadpoint string
	created   int64
}

// slotTimes returns the number of distinct slots from since in schema
func slotCount(ctx context.Context, q queryer, schema string, since time.Time) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(DISTINCT ts) FROM "+tableOf(schema, "meters")+" WHERE ts >= ?", since.Unix()).Scan(&n)
	return n, err
}

// latest returns the end of the recording in schema: the end of the last
// quarter hour in meters or the creation of the last charging session
func latest(ctx context.Context, q queryer, schema string) (time.Time, error) {
	var res time.Time

	var ts sql.NullInt64
	if err := q.QueryRowContext(ctx, "SELECT MAX(ts) FROM "+tableOf(schema, "meters")).Scan(&ts); err != nil {
		return res, err
	}
	if ts.Valid {
		res = time.Unix(ts.Int64, 0).Add(slot)
	}

	times, err := sessionTimes(ctx, q, schema)
	if err != nil {
		return res, err
	}
	for _, k := range times {
		if t := time.Unix(0, k.created); t.After(res) {
			res = t
		}
	}

	return res, nil
}

// columns returns the columns of a table
func columns(ctx context.Context, q queryer, schema, table string) ([]string, error) {
	pragma := "PRAGMA table_info(" + table + ")"
	if schema != "" {
		pragma = "PRAGMA " + schema + ".table_info(" + table + ")"
	}

	rows, err := q.QueryContext(ctx, pragma)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []string
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     sql.NullString
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		res = append(res, name)
	}

	return res, rows.Err()
}

// commonColumns returns the columns main.table and src.table share, minus skip,
// quoted for a column list
func commonColumns(ctx context.Context, q queryer, table string, skip ...string) (string, error) {
	dst, err := columns(ctx, q, "main", table)
	if err != nil {
		return "", err
	}
	src, err := columns(ctx, q, "src", table)
	if err != nil {
		return "", err
	}

	var cols []string
	for _, c := range dst {
		if contains(src, c) && !contains(skip, c) {
			cols = append(cols, `"`+c+`"`)
		}
	}

	if len(cols) == 0 {
		return "", fmt.Errorf("table %s missing", table)
	}

	return strings.Join(cols, ","), nil
}

func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}
