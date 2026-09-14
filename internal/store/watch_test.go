package store

import (
	"testing"

	"github.com/scastillo/wa/internal/fixture"
)

func TestSeedCursorUsesTheLargerOfZMaxAndTheNewestRow(t *testing.T) {
	fx := fixture.New(t)
	seedMessages(t, fx)
	s := openStore(t, fx)

	got, err := s.SeedCursor()
	if err != nil || got != 30 {
		t.Fatalf("without a Z_PRIMARYKEY row: got %d %v, want 30", got, err)
	}

	// Core Data never reuses a primary key, so Z_MAX can sit above the newest live row.
	fixture.Exec(t, fx.ChatStorage, "INSERT INTO Z_PRIMARYKEY (Z_ENT, Z_NAME, Z_SUPER, Z_MAX) VALUES (9, 'WAMessage', 0, 120)")
	again := openStore(t, fx)
	if got, err := again.SeedCursor(); err != nil || got != 120 {
		t.Fatalf("with Z_MAX 120: got %d %v", got, err)
	}
}

func TestMessagesAfterFollowArrivalOrderAcrossChats(t *testing.T) {
	fx := fixture.New(t)
	seedMessages(t, fx)
	s := openStore(t, fx)
	chats := map[int64]Chat{
		1: {PK: 1, JID: "111@g.us", Name: "Family", Kind: KindGroup},
		2: {PK: 2, JID: "5731@s.whatsapp.net", Name: "Ana", Kind: KindDM},
	}

	msgs, err := s.MessagesAfter(15, chats, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 20 is dated before 16..19 but arrived after them, so it follows them.
	if got := pks(msgs); got != "16 17 18 19 20 30" {
		t.Fatalf("arrival order: got %s", got)
	}
	if msgs[len(msgs)-1].ChatName != "Ana" || msgs[0].ChatJID != "111@g.us" {
		t.Fatalf("chat fields: %+v / %+v", msgs[0], msgs[len(msgs)-1])
	}

	msgs, err = s.MessagesAfter(15, chats, 2)
	if err != nil || pks(msgs) != "16 17" {
		t.Fatalf("limit keeps the oldest new rows first: %s %v", pks(msgs), err)
	}

	msgs, err = s.MessagesAfter(29, map[int64]Chat{}, 0)
	if err != nil || pks(msgs) != "30" || msgs[0].ChatJID != "" {
		t.Fatalf("a chat missing from the map has no JID: %s %+v %v", pks(msgs), msgs, err)
	}
}
