package store

import (
	"database/sql"
	"time"
)

// NewestMessage returns the time of the newest message that is not dated more than
// a day after now. It returns the zero time when there are no messages.
func (s *Store) NewestMessage(now time.Time) (time.Time, error) {
	var v sql.NullFloat64
	err := Retry(func() error {
		return s.Chat.QueryRow("SELECT max(ZMESSAGEDATE) FROM ZWAMESSAGE WHERE ZMESSAGEDATE <= ?",
			ToCoreData(now.Add(24*time.Hour))).Scan(&v)
	})
	if err != nil || !v.Valid {
		return time.Time{}, err
	}
	return FromCoreData(v.Float64), nil
}
