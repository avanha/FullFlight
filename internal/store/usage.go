package store

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// TableUsage is one table's row count and the disk it takes, indexes included.
type TableUsage struct {
	Name  string
	Rows  int
	Bytes int64
}

// AircraftUsage is how much one airframe's history weighs in rows.
type AircraftUsage struct {
	Hex       string
	Tail      string
	Messages  int
	Positions int
	Notes     int
}

// Usage describes what the database holds and how big it is.
type Usage struct {
	DBBytes   int64 // the database file, from its pages
	FreeBytes int64 // empty pages: what Compact would give back
	Tables    []TableUsage
	Oldest    time.Time // the oldest message or position; zero if there are none
	Newest    time.Time
	Aircraft  []AircraftUsage // busiest first
}

var usageTables = []string{"message", "position", "airframe", "airframe_note", "watch", "fix", "fetch_state"}

// Usage measures the database.
func (s *Store) Usage() (Usage, error) {
	var u Usage
	var pages, size, free int64
	s.db.QueryRow(`PRAGMA page_count`).Scan(&pages)
	s.db.QueryRow(`PRAGMA page_size`).Scan(&size)
	s.db.QueryRow(`PRAGMA freelist_count`).Scan(&free)
	u.DBBytes, u.FreeBytes = pages*size, free*size

	// disk per table: the table's own pages plus its indexes'
	owner := map[string]string{}
	rows, err := s.db.Query(`SELECT name, tbl_name FROM sqlite_schema WHERE type IN ('table','index')`)
	if err != nil {
		return u, err
	}
	for rows.Next() {
		var name, tbl string
		if err := rows.Scan(&name, &tbl); err != nil {
			rows.Close()
			return u, err
		}
		owner[name] = tbl
	}
	rows.Close()
	bytes := map[string]int64{}
	rows, err = s.db.Query(`SELECT name, SUM(pgsize) FROM dbstat GROUP BY name`)
	if err != nil {
		return u, err
	}
	for rows.Next() {
		var name string
		var b int64
		if err := rows.Scan(&name, &b); err != nil {
			rows.Close()
			return u, err
		}
		bytes[owner[name]] += b
	}
	rows.Close()
	for _, t := range usageTables {
		tu := TableUsage{Name: t, Bytes: bytes[t]}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + t).Scan(&tu.Rows); err != nil {
			return u, err
		}
		u.Tables = append(u.Tables, tu)
	}
	sort.SliceStable(u.Tables, func(i, j int) bool { return u.Tables[i].Bytes > u.Tables[j].Bytes })

	var lo, hi *int64
	s.db.QueryRow(`SELECT MIN(t), MAX(t) FROM (SELECT ts AS t FROM message UNION ALL SELECT ts FROM position)`).Scan(&lo, &hi)
	if lo != nil && hi != nil {
		u.Oldest, u.Newest = time.Unix(*lo, 0).UTC(), time.Unix(*hi, 0).UTC()
	}

	per := map[string]*AircraftUsage{}
	get := func(h string) *AircraftUsage {
		if per[h] == nil {
			per[h] = &AircraftUsage{Hex: h}
		}
		return per[h]
	}
	for _, q := range []struct {
		sql string
		set func(a *AircraftUsage, n int)
	}{
		{`SELECT hex, COUNT(*) FROM message GROUP BY hex`, func(a *AircraftUsage, n int) { a.Messages = n }},
		{`SELECT hex, COUNT(*) FROM position GROUP BY hex`, func(a *AircraftUsage, n int) { a.Positions = n }},
		{`SELECT hex, COUNT(*) FROM airframe_note GROUP BY hex`, func(a *AircraftUsage, n int) { a.Notes = n }},
	} {
		rs, err := s.db.Query(q.sql)
		if err != nil {
			return u, err
		}
		for rs.Next() {
			var h string
			var n int
			if err := rs.Scan(&h, &n); err != nil {
				rs.Close()
				return u, err
			}
			q.set(get(h), n)
		}
		rs.Close()
	}
	for _, a := range per {
		a.Tail, _ = s.TailForHex(a.Hex)
		u.Aircraft = append(u.Aircraft, *a)
	}
	sort.Slice(u.Aircraft, func(i, j int) bool {
		x, y := u.Aircraft[i], u.Aircraft[j]
		if x.Messages+x.Positions != y.Messages+y.Positions {
			return x.Messages+x.Positions > y.Messages+y.Positions
		}
		return x.Hex < y.Hex
	})
	return u, nil
}

// Bytes formats a size for people: "1.7 MB".
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// Report is the usage as text.
func (u Usage) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Database: %s", Bytes(u.DBBytes))
	if u.FreeBytes > 0 {
		fmt.Fprintf(&b, " (%s of it empty, reclaimed by compacting)", Bytes(u.FreeBytes))
	}
	b.WriteString("\n")
	for _, t := range u.Tables {
		fmt.Fprintf(&b, "  %-14s %9d rows  %10s\n", t.Name, t.Rows, Bytes(t.Bytes))
	}
	if !u.Oldest.IsZero() {
		days := u.Newest.Sub(u.Oldest).Hours() / 24
		fmt.Fprintf(&b, "Messages and positions span %s to %s (%.1f days)\n", u.Oldest.Format("2006-01-02 15:04Z"), u.Newest.Format("2006-01-02 15:04Z"), days)
	}
	if len(u.Aircraft) > 0 {
		b.WriteString("By aircraft:\n")
		for _, a := range u.Aircraft {
			name := a.Hex
			if a.Tail != "" {
				name = a.Tail + " " + a.Hex
			}
			notes := ""
			if a.Notes > 0 {
				notes = fmt.Sprintf("  (%d note%s: its data is never purged)", a.Notes, map[bool]string{true: "s", false: ""}[a.Notes > 1])
			}
			fmt.Fprintf(&b, "  %-16s %7d messages %7d positions%s\n", name, a.Messages, a.Positions, notes)
		}
	}
	return b.String()
}

// PurgeResult is what Purge removed, or with a dry run would have.
type PurgeResult struct {
	Cutoff    time.Time
	DryRun    bool
	Positions int
	Messages  int
	Watches   int
	// What was old enough to go but was kept because its airframe has notes.
	KeptAirframes int
	KeptPositions int
	KeptMessages  int
}

// An airframe with a note keeps everything: its messages and positions, and any watch on it
// (found by hex, or by registration for a watch that never learned the hex).
const (
	notedHexes  = `(SELECT hex FROM airframe_note)`
	notedTails  = `(SELECT tail FROM airframe WHERE hex IN (SELECT hex FROM airframe_note))`
	watchIsFree = `(hex = '' OR hex NOT IN ` + notedHexes + `) AND tail NOT IN ` + notedTails
)

// Purge removes the ADS-B positions and datalink messages received before cutoff, and finished watches
// that ended before it, for every airframe that has no notes. It never touches the airframe table, the
// notes or the learned waypoints (tiny, and wanted for good). With dryRun it only counts. The caller
// should Compact afterwards to give the disk space back.
func (s *Store) Purge(cutoff time.Time, dryRun bool) (PurgeResult, error) {
	r := PurgeResult{Cutoff: cutoff.UTC(), DryRun: dryRun}
	c := ts(cutoff)
	tx, err := s.db.Begin()
	if err != nil {
		return r, err
	}
	defer tx.Rollback()

	count := func(q string, args ...any) (n int) { tx.QueryRow(q, args...).Scan(&n); return }
	r.KeptPositions = count(`SELECT COUNT(*) FROM position WHERE ts < ? AND hex IN `+notedHexes, c)
	r.KeptMessages = count(`SELECT COUNT(*) FROM message WHERE ts < ? AND hex IN `+notedHexes, c)
	r.KeptAirframes = count(`SELECT COUNT(DISTINCT hex) FROM (
		SELECT hex FROM position WHERE ts < ?1 AND hex IN `+notedHexes+` UNION SELECT hex FROM message WHERE ts < ?1 AND hex IN `+notedHexes+`)`, c)

	steps := []struct {
		into *int
		del  string
		cnt  string
	}{
		{&r.Positions, `DELETE FROM position WHERE ts < ? AND hex NOT IN ` + notedHexes, `SELECT COUNT(*) FROM position WHERE ts < ? AND hex NOT IN ` + notedHexes},
		{&r.Messages, `DELETE FROM message WHERE ts < ? AND hex NOT IN ` + notedHexes, `SELECT COUNT(*) FROM message WHERE ts < ? AND hex NOT IN ` + notedHexes},
		{&r.Watches, `DELETE FROM watch WHERE status IN ('done','expired') AND end_ts < ? AND ` + watchIsFree,
			`SELECT COUNT(*) FROM watch WHERE status IN ('done','expired') AND end_ts < ? AND ` + watchIsFree},
	}
	for _, st := range steps {
		if dryRun {
			*st.into = count(st.cnt, c)
			continue
		}
		res, err := tx.Exec(st.del, c)
		if err != nil {
			return r, err
		}
		n, _ := res.RowsAffected()
		*st.into = int(n)
	}
	if dryRun {
		return r, nil
	}
	return r, tx.Commit()
}

// Compact returns freed space to the disk. It needs the database to itself, so it can fail with the
// app running; the purge itself is already done by then and the space is simply reused later.
func (s *Store) Compact() error {
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`VACUUM`)
	return err
}

// Report is the result as text.
func (r PurgeResult) Report() string {
	var b strings.Builder
	verb := "removed"
	if r.DryRun {
		verb = "would be removed"
		fmt.Fprintf(&b, "DRY RUN, nothing deleted. ")
	}
	fmt.Fprintf(&b, "Data received before %s:\n", r.Cutoff.Format("2006-01-02 15:04Z"))
	fmt.Fprintf(&b, "  %7d positions %s\n  %7d messages %s\n  %7d finished watches %s\n", r.Positions, verb, r.Messages, verb, r.Watches, verb)
	if r.KeptAirframes > 0 {
		fmt.Fprintf(&b, "Kept, because their airframe has notes: %d airframe%s with %d old positions and %d old messages.\n",
			r.KeptAirframes, map[bool]string{true: "s", false: ""}[r.KeptAirframes > 1], r.KeptPositions, r.KeptMessages)
	}
	b.WriteString("Airframes, notes and learned waypoints are never purged.\n")
	return b.String()
}
