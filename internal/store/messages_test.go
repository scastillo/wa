package store

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scastillo/wa/internal/fixture"
)

var t0 = time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

func liveURL(expires time.Time) string {
	v := url.Values{"oe": {fmt.Sprintf("%X", expires.Unix())}}
	return "https://media.example.invalid/v/t62/x.enc?" + v.Encode()
}

// seedMessages builds one group and one DM that cover every sender and media rule.
func seedMessages(t *testing.T, fx *fixture.Set) {
	t.Helper()
	at := func(d time.Duration) float64 { return ToCoreData(t0.Add(d)) }
	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWACHATSESSION (Z_PK, ZCONTACTJID, ZPARTNERNAME, ZSESSIONTYPE, ZLASTMESSAGEDATE) VALUES
		(1, '111@g.us', 'Family', 1, ?), (2, '5731@s.whatsapp.net', 'Ana', 0, ?)`, at(0), at(0))
	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWAGROUPMEMBER (Z_PK, ZMEMBERJID, ZCONTACTNAME, ZCHATSESSION) VALUES
		(1, '5732@s.whatsapp.net', '+57 300 000', 1), (2, '777@lid', '+57 301 000', 1), (3, '999@lid', '', 1)`)
	// Measured 2026-09-14: ZCONTACTNAME is '' on every real member row, so member 3
	// mirrors reality and must fall through to the message push name.
	fixture.Exec(t, fx.ContactsV2, `INSERT INTO ZWAADDRESSBOOKCONTACT (Z_PK, ZWHATSAPPID, ZLID, ZFULLNAME) VALUES
		(1, '5732@s.whatsapp.net', '777@lid', 'Carlos')`)
	// Measured 2026-09-14: ZWAMESSAGE.ZPUSHNAME is an ID on almost every incoming
	// row; the real push names live in ZWAPROFILEPUSHNAME (almost all name-like).
	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWAPROFILEPUSHNAME (Z_PK, ZJID, ZPUSHNAME) VALUES (1, '999@lid', 'Tio')`)

	onDisk := filepath.Join(fx.Dir, "Message", "Media", "111@g.us", "a.jpg")
	if err := os.MkdirAll(filepath.Dir(onDisk), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onDisk, []byte("jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWAMEDIAITEM (Z_PK, ZMEDIALOCALPATH, ZMEDIAURL, ZFILESIZE, ZTITLE) VALUES
		(1, 'Media/111@g.us/a.jpg', ?, 4, NULL),
		(2, NULL, ?, 1258291, NULL),
		(3, NULL, ?, 2048, 'contract.pdf'),
		(4, 'Media/111@g.us/gone.jpg', NULL, 10, NULL),
		(5, NULL, NULL, 0, NULL),
		(6, NULL, ?, 0, NULL)`,
		liveURL(t0.Add(-time.Hour)), liveURL(time.Now().Add(72*time.Hour)), liveURL(time.Now().Add(-time.Hour)),
		liveURL(time.Now().Add(72*time.Hour)))
	// Measured 2026-09-14: text messages carry an empty media item (no link, no
	// path, size 0) and link messages carry one with a link but size 0. Neither is
	// an attachment. Items 5 and 6 mirror them.

	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWAMESSAGE
		(Z_PK, ZCHATSESSION, ZMESSAGEDATE, ZISFROMME, ZFROMJID, ZGROUPMEMBER, ZPUSHNAME, ZTEXT, ZMESSAGETYPE, ZMEDIAITEM) VALUES
		(10, 1, ?, 1, NULL,        NULL, NULL,  'hola',                 0, NULL),
		(11, 1, ?, 0, '111@g.us',  1,    'c',   'line1'||char(10)||'line2', 0, NULL),
		(12, 1, ?, 0, '111@g.us',  3,    'CMPPodUGIAA=', NULL,          1, 1),
		(13, 1, ?, 0, '111@g.us',  2,    'c2',  'see this',             1, 2),
		(20, 1, ?, 0, '111@g.us',  1,    'c',   'late history row',     0, NULL),
		(15, 1, ?, 0, '111@g.us',  NULL, NULL,  NULL,                   10, NULL),
		(16, 1, ?, 0, '111@g.us',  NULL, 'Pepe','no member row',        0, NULL),
		(17, 1, ?, 0, '111@g.us',  3,    'CMLPodUGIAA=', NULL,          1, 4),
		(18, 1, ?, 0, '111@g.us',  1,    'c',   'plain text',           0, 5),
		(19, 1, ?, 0, '111@g.us',  1,    'c',   'https://example.invalid', 7, 6),
		(30, 2, ?, 0, '5731@s.whatsapp.net', NULL, 'ana push', 'contract attached', 8, 3)`,
		at(-7*time.Minute), at(-6*time.Minute), at(-5*time.Minute), at(-4*time.Minute),
		at(-10*time.Minute), at(-3*time.Minute), at(-2*time.Minute), at(-time.Minute),
		at(-8*time.Minute), at(-9*time.Minute), at(0))
}

func TestMessagesResolveSendersAndMediaInDateOrder(t *testing.T) {
	fx := fixture.New(t)
	seedMessages(t, fx)
	s := openStore(t, fx)

	msgs, err := s.Messages(Chat{PK: 1, JID: "111@g.us", Name: "Family", Kind: KindGroup}, Query{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range msgs {
		media := "-"
		if m.Media != nil {
			media = fmt.Sprintf("#%d/%s", m.Media.PK, m.Media.State)
		}
		got = append(got, fmt.Sprintf("%d:%s:%s", m.PK, m.Sender, media))
	}
	want := []string{
		"20:Carlos:-",            // late arrival sorts by its own date
		"19:Carlos:-",            // link preview item (size 0) is not an attachment
		"18:Carlos:-",            // empty media item on a text message is not an attachment
		"10:me:-",                // own message
		"11:Carlos:-",            // address book beats the group contact name
		"12:Tio:#1/on-disk",      // not in the book: profile push name, never the message ID
		"13:Carlos:#2/live-link", // LID matched through the book
		"15:system:-",            // system row (type 10) with no sender
		"16:unknown:-",           // a message push name is an ID, never a name
		"17:Tio:#4/phone-only",   // local path set but the file is gone
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("messages:\n got  %v\n want %v", got, want)
	}
	byPK := map[int64]Message{}
	for _, m := range msgs {
		byPK[m.PK] = m
	}
	if byPK[11].Text != "line1\nline2" || byPK[11].Type != 0 || !byPK[10].FromMe || byPK[19].Type != 7 {
		t.Fatalf("fields not carried: %+v %+v %+v", byPK[10], byPK[11], byPK[19])
	}
}

func TestMessagesInADMUseTheChatNameAndExpiredLinks(t *testing.T) {
	fx := fixture.New(t)
	seedMessages(t, fx)
	s := openStore(t, fx)

	msgs, err := s.Messages(Chat{PK: 2, JID: "5731@s.whatsapp.net", Name: "Ana", Kind: KindDM}, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Sender != "Ana" || msgs[0].Media == nil ||
		msgs[0].Media.State != MediaPhoneOnly || msgs[0].Media.Title != "contract.pdf" || msgs[0].Media.Size != 2048 {
		t.Fatalf("dm message: %+v media %+v", msgs, msgs[0].Media)
	}
}

func TestMessagesFilterByDateAndKeepTheNewestWithinTheLimit(t *testing.T) {
	fx := fixture.New(t)
	seedMessages(t, fx)
	s := openStore(t, fx)
	group := Chat{PK: 1, JID: "111@g.us", Kind: KindGroup}

	msgs, err := s.Messages(group, Query{Since: t0.Add(-5 * time.Minute), Until: t0.Add(-2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if pks(msgs) != "12 13 15" { // Until is exclusive
		t.Fatalf("since/until: got %s", pks(msgs))
	}

	msgs, err = s.Messages(group, Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if pks(msgs) != "16 17" {
		t.Fatalf("limit keeps the newest, in date order: got %s", pks(msgs))
	}
}

func TestSearchOnlyLooksInTheGivenChats(t *testing.T) {
	fx := fixture.New(t)
	seedMessages(t, fx)
	s := openStore(t, fx)
	family := Chat{PK: 1, JID: "111@g.us", Name: "Family", Kind: KindGroup}
	ana := Chat{PK: 2, JID: "5731@s.whatsapp.net", Name: "Ana", Kind: KindDM}

	hits, err := s.Search("CONTRACT", []Chat{family, ana}, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if pks(hits) != "30" || hits[0].ChatName != "Ana" {
		t.Fatalf("case-insensitive hit across chats: %s %+v", pks(hits), hits)
	}

	hits, err = s.Search("contract", []Chat{family}, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("a chat outside the given set must not match: %s", pks(hits))
	}

	hits, err = s.Search("contract", nil, Query{})
	if err != nil || len(hits) != 0 {
		t.Fatalf("no chats means no results: %s %v", pks(hits), err)
	}

	hits, err = s.Search("%", []Chat{family, ana}, Query{})
	if err != nil || len(hits) != 0 {
		t.Fatalf("%% must be a literal, not a wildcard: %s %v", pks(hits), err)
	}
}

func pks(msgs []Message) string {
	var s []string
	for _, m := range msgs {
		s = append(s, fmt.Sprint(m.PK))
	}
	return strings.Join(s, " ")
}
