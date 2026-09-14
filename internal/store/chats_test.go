package store

import (
	"strings"
	"testing"
	"time"

	"github.com/scastillo/wa/internal/fixture"
)

// coreData converts a wall time to WhatsApp's Core Data timestamp (seconds since 2001-01-01 UTC).
func coreData(t time.Time) float64 {
	return float64(t.Unix()-coreDataEpoch) + float64(t.Nanosecond())/1e9
}

func seedChats(t *testing.T, fx *fixture.Set) {
	t.Helper()
	last := coreData(time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC))
	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWACHATSESSION
		(Z_PK, ZCONTACTJID, ZPARTNERNAME, ZSESSIONTYPE, ZLASTMESSAGEDATE, ZUNREADCOUNT, ZREMOVED, ZHIDDEN) VALUES
		(1, '111@g.us',            'Family',        1, ?, 3, 0, 0),
		(2, '5731@s.whatsapp.net', 'Ana',           0, ?, 0, 0, 0),
		(3, '999@lid',             'Family Doctor', 0, ?, 0, 0, 0),
		(4, '222@g.us',            'Old group',     1, ?, 0, 1, 0),
		(5, '0@status',            NULL,            3, ?, 0, 0, 0)`,
		last, last-60, last-120, last-180, last-240)
}

func TestChatsListsLiveSessionsNewestFirst(t *testing.T) {
	fx := fixture.New(t)
	seedChats(t, fx)
	s := openStore(t, fx)

	chats, err := s.Chats()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range chats {
		got = append(got, c.JID+"|"+string(c.Kind))
	}
	want := "111@g.us|group 5731@s.whatsapp.net|dm 999@lid|dm 0@status|status"
	if strings.Join(got, " ") != want {
		t.Fatalf("chats:\n got  %s\n want %s", strings.Join(got, " "), want)
	}
	if chats[0].Unread != 3 || !chats[0].LastMessageAt.Equal(time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("first chat fields: %+v", chats[0])
	}
}

func TestResolveChat(t *testing.T) {
	chats := []Chat{
		{PK: 1, JID: "111@g.us", Name: "Family"},
		{PK: 2, JID: "5731@s.whatsapp.net", Name: "Ana"},
		{PK: 3, JID: "999@lid", Name: "Family Doctor"},
	}
	cases := map[string][]int64{
		"111@g.us":  {1},    // exact JID
		"2":         {2},    // row number
		"ana":       {2},    // name part, any case
		"family":    {1, 3}, // ambiguous
		"doctor":    {3},
		"nobody":    nil,
		"999@lid":   {3},
		"  Family ": {1, 3}, // trimmed
	}
	for q, want := range cases {
		got := Resolve(chats, q)
		var pks []int64
		for _, c := range got {
			pks = append(pks, c.PK)
		}
		if len(pks) != len(want) {
			t.Errorf("%q: got %v, want %v", q, pks, want)
			continue
		}
		for i := range want {
			if pks[i] != want[i] {
				t.Errorf("%q: got %v, want %v", q, pks, want)
			}
		}
	}
}

func TestCheckSchemaPassesOnTheDump(t *testing.T) {
	fx := fixture.New(t)
	s := openStore(t, fx)
	if err := s.CheckSchema(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSchemaNamesEveryMissingColumn(t *testing.T) {
	fx := fixture.New(t)
	fixture.Exec(t, fx.ChatStorage, "ALTER TABLE ZWAMEDIAITEM RENAME COLUMN ZMEDIALOCALPATH TO ZLOCALPATH2")
	fixture.Exec(t, fx.ContactsV2, "ALTER TABLE ZWAADDRESSBOOKCONTACT RENAME COLUMN ZFULLNAME TO ZNAME2")
	s := openStore(t, fx)

	err := s.CheckSchema()
	if err == nil {
		t.Fatal("renamed columns must fail the schema check")
	}
	for _, want := range []string{"schema drift", "ZWAMEDIAITEM.ZMEDIALOCALPATH", "ZWAADDRESSBOOKCONTACT.ZFULLNAME"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must contain %q", err, want)
		}
	}
}

func openStore(t *testing.T, fx *fixture.Set) *Store {
	t.Helper()
	s, err := OpenStore(fx.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
