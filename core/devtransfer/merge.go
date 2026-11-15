package devtransfer

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/evcc-io/evcc/core/keys"
)

// Items selects what is transferred
type Items struct {
	Sessions bool `json:"sessions"`
	Energy   bool `json:"energy"`
	Peak     bool `json:"peak"`
	Devices  bool `json:"devices"`
	Settings bool `json:"settings"`
}

// ParseItems reads a comma separated list of item names
func ParseItems(s string) (Items, error) {
	var it Items
	for _, name := range strings.Split(s, ",") {
		switch strings.TrimSpace(name) {
		case "sessions":
			it.Sessions = true
		case "energy":
			it.Energy = true
		case "peak":
			it.Peak = true
		case "devices":
			it.Devices = true
		case "settings":
			it.Settings = true
		case "":
		default:
			return it, fmt.Errorf("unknown item %q", name)
		}
	}

	if it == (Items{}) {
		return it, fmt.Errorf("no item selected")
	}

	return it, nil
}

// Names returns the selected items as the list ParseItems takes
func (it Items) Names() []string {
	var res []string
	for _, e := range []struct {
		on   bool
		name string
	}{{it.Sessions, "sessions"}, {it.Energy, "energy"}, {it.Peak, "peak"}, {it.Devices, "devices"}, {it.Settings, "settings"}} {
		if e.on {
			res = append(res, e.name)
		}
	}
	return res
}

// DefaultItems is the selection the give back starts with
var DefaultItems = Items{Sessions: true, Energy: true, Peak: true}

// Applied is the record of the last give back, stored in the real database
type Applied struct {
	At    time.Time `json:"at"`
	Items []string  `json:"items"`
}

// Counts is the number of rows a merge added
type Counts struct {
	Sessions int `json:"sessions"`
	Slots    int `json:"slots"`
	Prices   int `json:"prices"`
	Months   int `json:"months"`
}

// merge adds what src recorded from since on to dst, in one transaction: the
// charging sessions, slots and prices that dst does not have, the monthly peak
// where src's is higher, and with the items set the devices and settings replaced
// by src's. dst and src are database files, each opened of its own connection;
// an existing entry of dst is never overwritten. applied, if set, is stored in dst.
func merge(ctx context.Context, dstPath, srcPath string, since time.Time, items Items, applied *Applied) (res Counts, err error) {
	dbc, err := openFile(dstPath)
	if err != nil {
		return res, err
	}
	defer dbc.Close()

	conn, err := dbc.Conn(ctx)
	if err != nil {
		return res, wrapBusy(err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS src", readOnlyURI(srcPath)); err != nil {
		return res, wrapBusy(err)
	}

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return res, wrapBusy(err)
	}

	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
			res = Counts{}
		}
	}()

	if items.Sessions {
		if res.Sessions, err = mergeSessions(ctx, conn, since); err != nil {
			return res, fmt.Errorf("sessions: %w", err)
		}
	}

	if items.Energy {
		if res.Slots, err = mergeSlots(ctx, conn, since); err != nil {
			return res, fmt.Errorf("energy: %w", err)
		}
		if res.Prices, err = mergePrices(ctx, conn, since); err != nil {
			return res, fmt.Errorf("prices: %w", err)
		}
	}

	if items.Peak {
		if res.Months, err = mergePeak(ctx, conn, since); err != nil {
			return res, fmt.Errorf("peak: %w", err)
		}
	}

	if items.Devices {
		if err = replaceDevices(ctx, conn); err != nil {
			return res, fmt.Errorf("devices: %w", err)
		}
	}

	if items.Settings {
		if err = replaceSettings(ctx, conn); err != nil {
			return res, fmt.Errorf("settings: %w", err)
		}
	}

	if applied != nil {
		b, _ := json.Marshal(applied)
		if _, err = conn.ExecContext(ctx, "INSERT OR REPLACE INTO main.settings (key, value) VALUES (?, ?)", DevTransferApplied, string(b)); err != nil {
			return res, fmt.Errorf("applied: %w", err)
		}
	}

	_, err = conn.ExecContext(ctx, "COMMIT")
	return res, wrapBusy(err)
}

// mergeSessions adds the charging sessions created from since on that dst does
// not have (same loadpoint and creation), without their id
func mergeSessions(ctx context.Context, conn *sql.Conn, since time.Time) (int, error) {
	have, err := sessionTimes(ctx, conn, "main")
	if err != nil {
		return 0, err
	}
	known := make(map[sessionKey]bool, len(have))
	for _, k := range have {
		known[k] = true
	}

	src, err := sessionTimes(ctx, conn, "src")
	if err != nil {
		return 0, err
	}

	cols, err := commonColumns(ctx, conn, "sessions", "id")
	if err != nil {
		return 0, err
	}

	var n int
	for id, k := range src {
		if k.created < since.UnixNano() || known[k] {
			continue
		}
		res, err := conn.ExecContext(ctx, fmt.Sprintf("INSERT INTO main.sessions (%s) SELECT %s FROM src.sessions WHERE id = ?", cols, cols), id)
		if err != nil {
			return n, err
		}
		rows, _ := res.RowsAffected()
		n += int(rows)
		known[k] = true
	}

	return n, nil
}

// mergeSlots adds the entities dst misses and the slots from since on of src
// that dst does not have, mapped over group and name. The last absolute readings
// of the entities follow, so evcc custom carries on from them.
func mergeSlots(ctx context.Context, conn *sql.Conn, since time.Time) (int, error) {
	// the entities already in dst take over the latest readings of src, if src
	// recorded later than dst; decided before the slots are added
	if _, err := conn.ExecContext(ctx, `UPDATE main.entities SET
			energy_meter = COALESCE((SELECT s.energy_meter FROM src.entities s WHERE s."group" = main.entities."group" AND s.name = main.entities.name), energy_meter),
			return_energy_meter = COALESCE((SELECT s.return_energy_meter FROM src.entities s WHERE s."group" = main.entities."group" AND s.name = main.entities.name), return_energy_meter)
		WHERE (SELECT MAX(m.ts) FROM src.entities s JOIN src.meters m ON m.meter = s.id
				WHERE s."group" = main.entities."group" AND s.name = main.entities.name AND m.ts >= ?)
			> COALESCE((SELECT MAX(x.ts) FROM main.meters x WHERE x.meter = main.entities.id), 0)`, slotSince(since)); err != nil {
		return 0, err
	}

	ecols, err := commonColumns(ctx, conn, "entities", "id")
	if err != nil {
		return 0, err
	}

	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`INSERT INTO main.entities (%s) SELECT %s FROM src.entities s
		WHERE NOT EXISTS (SELECT 1 FROM main.entities d WHERE d."group" = s."group" AND d.name = s.name)`, ecols, ecols)); err != nil {
		return 0, err
	}

	mcols, err := commonColumns(ctx, conn, "meters", "meter")
	if err != nil {
		return 0, err
	}

	prefixed := "s." + strings.ReplaceAll(mcols, ",", ",s.")

	res, err := conn.ExecContext(ctx, fmt.Sprintf(`INSERT INTO main.meters (meter, %s)
		SELECT d.id, %s FROM src.meters s
		JOIN src.entities se ON se.id = s.meter
		JOIN main.entities d ON d."group" = se."group" AND d.name = se.name
		WHERE s.ts >= ? AND NOT EXISTS (SELECT 1 FROM main.meters x WHERE x.meter = d.id AND x.ts = s.ts)`, mcols, prefixed), slotSince(since))
	if err != nil {
		return 0, err
	}
	rows, _ := res.RowsAffected()

	return int(rows), nil
}

// mergePrices adds the prices of the slots from since on that dst does not have
func mergePrices(ctx context.Context, conn *sql.Conn, since time.Time) (int, error) {
	var n int
	for _, table := range []string{"tariffs", "tariffs_eeg"} {
		cols, err := commonColumns(ctx, conn, table)
		if err != nil {
			return n, err
		}

		res, err := conn.ExecContext(ctx, fmt.Sprintf(`INSERT INTO main.%[1]s (%[2]s) SELECT %[2]s FROM src.%[1]s s
			WHERE s.ts >= ? AND NOT EXISTS (SELECT 1 FROM main.%[1]s d WHERE d.ts = s.ts)`, table, cols), slotSince(since))
		if err != nil {
			return n, err
		}
		rows, _ := res.RowsAffected()
		n += int(rows)
	}
	return n, nil
}

// peakMonth is an entry of the monthly peak statistics, other fields kept as they are
type peakMonth map[string]any

func (m peakMonth) month() string { s, _ := m["month"].(string); return s }
func (m peakMonth) peak() float64 { f, _ := m["peak"].(float64); return f }

// settingValue reads a settings entry of a schema
func settingValue(ctx context.Context, q queryer, schema, key string) (string, bool, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT value FROM "+tableOf(schema, "settings")+" WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return v, err == nil, err
}

func readMonths(ctx context.Context, q queryer, schema string) ([]peakMonth, error) {
	v, ok, err := settingValue(ctx, q, schema, keys.PeakMonths)
	if err != nil || !ok {
		return nil, err
	}

	var res []peakMonth
	if err := json.Unmarshal([]byte(v), &res); err != nil {
		return nil, fmt.Errorf("%s: %w", keys.PeakMonths, err)
	}
	return res, nil
}

// sinceMonth is the month the test time began in
func sinceMonth(since time.Time) string {
	return since.Local().Format("2006-01")
}

// mergePeak takes over the months from the month of since on: a month dst does
// not have is added, otherwise the entry with the higher peak is kept
func mergePeak(ctx context.Context, conn *sql.Conn, since time.Time) (int, error) {
	src, err := readMonths(ctx, conn, "src")
	if err != nil {
		return 0, err
	}
	dst, err := readMonths(ctx, conn, "main")
	if err != nil {
		return 0, err
	}

	var n int
	for _, m := range src {
		if m.month() < sinceMonth(since) {
			continue
		}

		i := slices.IndexFunc(dst, func(e peakMonth) bool { return e.month() == m.month() })
		switch {
		case i < 0:
			dst = append(dst, m)
		case m.peak() > dst[i].peak():
			dst[i] = m
		default:
			continue
		}
		n++
	}

	if n == 0 {
		return 0, nil
	}

	slices.SortStableFunc(dst, func(a, b peakMonth) int { return cmp.Compare(b.month(), a.month()) })

	b, err := json.Marshal(dst)
	if err != nil {
		return 0, err
	}

	_, err = conn.ExecContext(ctx, "INSERT OR REPLACE INTO main.settings (key, value) VALUES (?, ?)", keys.PeakMonths, string(b))
	return n, err
}

// replaceDevices replaces the devices of dst by those of src
func replaceDevices(ctx context.Context, conn *sql.Conn) error {
	cols, err := commonColumns(ctx, conn, "configs")
	if err != nil {
		return err
	}

	if _, err := conn.ExecContext(ctx, "DELETE FROM main.configs"); err != nil {
		return err
	}

	_, err = conn.ExecContext(ctx, fmt.Sprintf("INSERT INTO main.configs (%[1]s) SELECT %[1]s FROM src.configs", cols))
	return err
}

// replaceSettings replaces the settings of dst by those of src, except the state keys
func replaceSettings(ctx context.Context, conn *sql.Conn) error {
	marks := strings.TrimSuffix(strings.Repeat("?,", len(stateKeys)), ",")
	args := make([]any, len(stateKeys))
	for i, k := range stateKeys {
		args[i] = k
	}

	if _, err := conn.ExecContext(ctx, "DELETE FROM main.settings WHERE key NOT IN ("+marks+")", args...); err != nil {
		return err
	}

	_, err := conn.ExecContext(ctx, "INSERT OR REPLACE INTO main.settings (key, value) SELECT key, value FROM src.settings WHERE key NOT IN ("+marks+")", args...)
	return err
}
