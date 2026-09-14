//go:build livegate

// Gate 0.4 of the plan: decrypt real WhatsApp media from the CDN with the stored key.
//
//	go test -tags livegate -count=1 -v -run TestGate04 ./internal/mediakey
//
// It reads the real WhatsApp Desktop data read-only and downloads a few encrypted
// files from the WhatsApp CDN into memory. It prints counts, sizes, field numbers
// and booleans only: no message text, names, URLs or file contents.
package mediakey

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/scastillo/wa/internal/store"
)

const (
	gatePerType  = 5
	gateMaxBytes = 16 << 20
)

func TestGate04(t *testing.T) {
	dir, err := store.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "ChatStorage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	client := &http.Client{Timeout: 90 * time.Second}
	types := []struct {
		msgType int
		typ     Type
	}{{1, Image}, {2, Video}, {3, Audio}, {8, Document}}

	for _, tc := range types {
		t.Run(tc.typ.String(), func(t *testing.T) {
			rows := gateCandidates(t, db, tc.msgType, dir)
			if len(rows) < gatePerType {
				t.Fatalf("only %d eligible files (on disk, live link, <= 16 MiB); need %d", len(rows), gatePerType)
			}
			var macOK, sizeOK, shaEqual, field1, field2 int
			for _, r := range rows[:gatePerType] {
				body := fetch(t, client, r.url)
				plain, field, err := Open(r.key, tc.typ, body)
				if err != nil {
					t.Errorf("pk %d: %v", r.pk, err)
					continue
				}
				macOK++
				if field == 1 {
					field1++
				} else {
					field2++
				}
				if int64(len(plain)) == r.size {
					sizeOK++
				} else {
					t.Errorf("pk %d: plaintext %d bytes, ZFILESIZE %d", r.pk, len(plain), r.size)
				}
				local, err := os.ReadFile(r.local)
				if err == nil && sha256.Sum256(local) == sha256.Sum256(plain) {
					shaEqual++
				}

				// Negative controls on real data: one flipped byte and the wrong info string must fail.
				flipped := bytes.Clone(body)
				flipped[len(flipped)/2] ^= 0x01
				if _, _, err := Open(r.key, tc.typ, flipped); !errors.Is(err, ErrBadMAC) {
					t.Errorf("pk %d: flipped byte must fail with ErrBadMAC, got %v", r.pk, err)
				}
				wrong := Document
				if tc.typ == Document {
					wrong = Image
				}
				if _, _, err := Open(r.key, wrong, body); !errors.Is(err, ErrBadMAC) {
					t.Errorf("pk %d: wrong info string must fail with ErrBadMAC, got %v", r.pk, err)
				}
			}
			t.Logf("%s: MAC ok %d/%d · size = ZFILESIZE %d/%d · sha256 = on-disk file %d/%d · key in field1 %d, field2 %d",
				tc.typ, macOK, gatePerType, sizeOK, gatePerType, shaEqual, gatePerType, field1, field2)
		})
	}
}

type gateRow struct {
	pk    int64
	url   string
	key   []byte
	size  int64
	local string
}

func gateCandidates(t *testing.T, db *store.DB, msgType int, dir string) []gateRow {
	t.Helper()
	q := `SELECT mi.Z_PK, mi.ZMEDIAURL, mi.ZMEDIAKEY, mi.ZFILESIZE, mi.ZMEDIALOCALPATH
	      FROM ZWAMEDIAITEM mi JOIN ZWAMESSAGE m ON m.ZMEDIAITEM = mi.Z_PK
	      WHERE m.ZMESSAGETYPE = ? AND mi.ZMEDIAURL IS NOT NULL AND mi.ZMEDIAKEY IS NOT NULL
	        AND mi.ZMEDIALOCALPATH IS NOT NULL AND mi.ZFILESIZE BETWEEN 1 AND ?
	      ORDER BY m.Z_PK DESC LIMIT 400`
	rows, err := db.Query(q, msgType, gateMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []gateRow
	now := time.Now().Add(5 * time.Minute).Unix()
	for rows.Next() {
		var r gateRow
		var rel sql.NullString
		if err := rows.Scan(&r.pk, &r.url, &r.key, &r.size, &rel); err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(r.url)
		if err != nil {
			continue
		}
		oe, err := strconv.ParseInt(u.Query().Get("oe"), 16, 64)
		if err != nil || oe <= now {
			continue
		}
		r.local = filepath.Join(dir, "Message", rel.String)
		if _, err := os.Stat(r.local); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

func fetch(t *testing.T, client *http.Client, raw string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://web.whatsapp.com")
	req.Header.Set("Referer", "https://web.whatsapp.com/")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET failed: %v", errors.Unwrap(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CDN status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, gateMaxBytes+1024))
	if err != nil {
		t.Fatal(err)
	}
	return body
}
