package store

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/scastillo/wa/internal/fixture"
)

// seedAttachments adds keys and stanza IDs to seedMessages, plus a sticker.
func seedAttachments(t *testing.T, fx *fixture.Set) {
	t.Helper()
	seedMessages(t, fx)
	for pk, key := range map[int]byte{1: 0x01, 2: 0x02, 3: 0x03, 4: 0x04} {
		fixture.Exec(t, fx.ChatStorage, "UPDATE ZWAMEDIAITEM SET ZMEDIAKEY = ? WHERE Z_PK = ?", bytes.Repeat([]byte{key}, 76), pk)
	}
	fixture.Exec(t, fx.ChatStorage, "UPDATE ZWAMESSAGE SET ZSTANZAID = 'ST' || Z_PK")
	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWAMEDIAITEM (Z_PK, ZMEDIALOCALPATH, ZMEDIAURL, ZFILESIZE) VALUES (7, 'Media/111@g.us/s.webp', NULL, 5)`)
	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWAMESSAGE (Z_PK, ZCHATSESSION, ZMESSAGEDATE, ZISFROMME, ZGROUPMEMBER, ZMESSAGETYPE, ZMEDIAITEM, ZSTANZAID)
		VALUES (21, 1, ?, 0, 1, 15, 7, 'ST21')`, ToCoreData(t0.Add(-30*time.Second)))
}

func attachmentPKs(atts []Attachment) string {
	var out []string
	for _, a := range atts {
		out = append(out, fmt.Sprintf("%d/%d", a.PK, a.Media.PK))
	}
	return strings.Join(out, " ")
}

func TestAttachmentsListRealFilesOfTheDefaultTypes(t *testing.T) {
	fx := fixture.New(t)
	seedAttachments(t, fx)
	s := openStore(t, fx)
	group := Chat{PK: 1, JID: "111@g.us", Name: "Family", Kind: KindGroup}

	atts, err := s.Attachments(group, AttachmentQuery{})
	if err != nil {
		t.Fatal(err)
	}
	// 18 and 19 carry size-0 items; 21 is a sticker, which needs an explicit type.
	if got := attachmentPKs(atts); got != "12/1 13/2 17/4" {
		t.Fatalf("attachments (message/media): got %q", got)
	}
	first := atts[0]
	if first.StanzaID != "ST12" || first.LocalPath != "Media/111@g.us/a.jpg" || first.Media.State != MediaOnDisk ||
		first.Sender != "Tio" || first.Type != 1 || first.Media.Size != 4 || !bytes.Equal(first.MediaKey, bytes.Repeat([]byte{1}, 76)) {
		t.Fatalf("fields not carried: %+v media %+v", first, first.Media)
	}
	if atts[1].URL == "" || atts[1].Media.State != MediaLiveLink || atts[2].Media.State != MediaPhoneOnly {
		t.Fatalf("states: %+v / %+v", atts[1].Media, atts[2].Media)
	}
}

func TestAttachmentsFilterByTypeDateAndLimit(t *testing.T) {
	fx := fixture.New(t)
	seedAttachments(t, fx)
	s := openStore(t, fx)
	group := Chat{PK: 1, JID: "111@g.us", Name: "Family", Kind: KindGroup}
	dm := Chat{PK: 2, JID: "5731@s.whatsapp.net", Name: "Ana", Kind: KindDM}

	cases := []struct {
		name string
		chat Chat
		q    AttachmentQuery
		want string
	}{
		{"sticker by type", group, AttachmentQuery{Types: []int{15}}, "21/7"},
		{"documents in the group", group, AttachmentQuery{Types: []int{8}}, ""},
		{"documents in the dm", dm, AttachmentQuery{Types: []int{8}}, "30/3"},
		{"limit keeps the newest", group, AttachmentQuery{Query: Query{Limit: 1}}, "17/4"},
		{"since", group, AttachmentQuery{Query: Query{Since: t0.Add(-4 * time.Minute)}}, "13/2 17/4"},
		{"until is exclusive", group, AttachmentQuery{Query: Query{Until: t0.Add(-4 * time.Minute)}}, "12/1"},
	}
	for _, c := range cases {
		atts, err := s.Attachments(c.chat, c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := attachmentPKs(atts); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
