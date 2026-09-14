package store

import (
	"testing"
	"time"

	"github.com/scastillo/wa/internal/fixture"
)

// Measured on real data 2026-09-14: some chat sessions carry a far-future last
// message date, so the session table cannot tell how fresh the data is.
func TestNewestMessageIgnoresFutureDates(t *testing.T) {
	fx := fixture.New(t)
	seedMessages(t, fx)
	future := ToCoreData(time.Date(4001, 1, 1, 0, 0, 0, 0, time.UTC))
	fixture.Exec(t, fx.ChatStorage, "UPDATE ZWACHATSESSION SET ZLASTMESSAGEDATE = ? WHERE Z_PK = 2", future)
	fixture.Exec(t, fx.ChatStorage, "INSERT INTO ZWAMESSAGE (Z_PK, ZCHATSESSION, ZMESSAGEDATE) VALUES (99, 2, ?)", future)
	s := openStore(t, fx)

	got, err := s.NewestMessage(t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(t0) {
		t.Fatalf("newest message: got %v, want %v", got, t0)
	}
}

// Measured 2026-09-14: 3 real sessions have dates in the years 8026, 9026 and one
// beyond what SQLite can format. Such a value is not a message time.
func TestChatsTreatFarFutureDatesAsUnknown(t *testing.T) {
	fx := fixture.New(t)
	seedMessages(t, fx)
	fixture.Exec(t, fx.ChatStorage, "UPDATE ZWACHATSESSION SET ZLASTMESSAGEDATE = ? WHERE Z_PK = 1",
		ToCoreData(time.Date(9026, 1, 1, 0, 0, 0, 0, time.UTC)))
	fixture.Exec(t, fx.ChatStorage, "UPDATE ZWACHATSESSION SET ZLASTMESSAGEDATE = 1e300 WHERE Z_PK = 2")
	s := openStore(t, fx)

	chats, err := s.Chats()
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 {
		t.Fatalf("both chats must still be listed, got %d", len(chats))
	}
	for _, c := range chats {
		if !c.LastMessageAt.IsZero() {
			t.Errorf("chat %d: far-future date must read as unknown, got %v", c.PK, c.LastMessageAt)
		}
	}
}

func TestNewestMessageOnEmptyData(t *testing.T) {
	fx := fixture.New(t)
	s := openStore(t, fx)
	got, err := s.NewestMessage(t0)
	if err != nil || !got.IsZero() {
		t.Fatalf("empty data: got %v %v, want zero time", got, err)
	}
}
