package store

// SeedCursor returns the row number that new messages will come after: the larger
// of Core Data's key counter (Z_PRIMARYKEY.Z_MAX for WAMessage) and the newest row.
// Core Data never reuses a key, so every future message gets a larger Z_PK.
func (s *Store) SeedCursor() (int64, error) {
	var n int64
	err := Retry(func() error {
		return s.Chat.QueryRow(`SELECT max(
			COALESCE((SELECT Z_MAX FROM Z_PRIMARYKEY WHERE Z_NAME = 'WAMessage'), 0),
			COALESCE((SELECT max(Z_PK) FROM ZWAMESSAGE), 0))`).Scan(&n)
	})
	return n, err
}

// MessagesAfter lists messages with Z_PK above cursor in arrival order, across all
// chats. It sorts by Z_PK, not by date: many real rows arrived after rows with a
// later date (measured 2026-09-14), and a date cursor would skip them for good.
// A message in a chat missing from chats comes back with an empty ChatJID.
func (s *Store) MessagesAfter(cursor int64, chats map[int64]Chat, limit int) ([]Message, error) {
	query := messageSelect + " WHERE m.Z_PK > ? ORDER BY m.Z_PK"
	args := []any{cursor}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	return s.messages(query, args, chats)
}
