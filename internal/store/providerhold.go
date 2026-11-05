package store

import (
	"database/sql"
	"time"
)

// SetProviderHold records that the named service is not to be asked until then, and why.
func (s *Store) SetProviderHold(name string, until time.Time, reason string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO provider_hold (name, until, reason) VALUES (?,?,?)`, name, ts(until), reason)
	return err
}

// ProviderHold returns when the named service may be asked again and why not before, if that is still in the future at now.
func (s *Store) ProviderHold(name string, now time.Time) (until time.Time, reason string, ok bool) {
	var u int64
	err := s.db.QueryRow(`SELECT until, reason FROM provider_hold WHERE name=?`, name).Scan(&u, &reason)
	if err == sql.ErrNoRows || err != nil || u <= ts(now) {
		return time.Time{}, "", false
	}
	return time.Unix(u, 0).UTC(), reason, true
}

// ClearProviderHold forgets a hold.
func (s *Store) ClearProviderHold(name string) error {
	_, err := s.db.Exec(`DELETE FROM provider_hold WHERE name=?`, name)
	return err
}

// ProviderHolds lists the holds still in the future at now: the services we are leaving alone, and why.
func (s *Store) ProviderHolds(now time.Time) (map[string]Hold, error) {
	rows, err := s.db.Query(`SELECT name, until, reason FROM provider_hold WHERE until > ?`, ts(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Hold{}
	for rows.Next() {
		var name, why string
		var u int64
		if err := rows.Scan(&name, &u, &why); err != nil {
			return nil, err
		}
		out[name] = Hold{Until: time.Unix(u, 0).UTC(), Reason: why}
	}
	return out, rows.Err()
}

// Hold is a service we were told to leave alone until Until.
type Hold struct {
	Until  time.Time
	Reason string
}
