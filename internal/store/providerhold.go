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
