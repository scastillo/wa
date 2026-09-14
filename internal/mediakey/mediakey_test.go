package mediakey

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

// RFC 5869 test case 3: SHA-256, empty salt, empty info. WhatsApp also uses an
// empty salt, so this vector pins the one HKDF detail a round trip cannot catch.
func TestHKDFMatchesRFC5869EmptySalt(t *testing.T) {
	ikm := bytes.Repeat([]byte{0x0b}, 22)
	want := "8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d9d201395faa4b61a96c8"
	got, err := expand(ikm, "", 42)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != want {
		t.Fatalf("HKDF output\n got  %x\n want %s", got, want)
	}
}

func TestInfoStrings(t *testing.T) {
	cases := map[Type]string{
		Image:    "WhatsApp Image Keys",
		Sticker:  "WhatsApp Image Keys",
		Video:    "WhatsApp Video Keys",
		Audio:    "WhatsApp Audio Keys",
		Document: "WhatsApp Document Keys",
	}
	for typ, want := range cases {
		if got := typ.info(); got != want {
			t.Errorf("%v: got %q, want %q", typ, got, want)
		}
	}
}

func TestCandidatesReadsTheTwo32ByteFields(t *testing.T) {
	f1 := bytes.Repeat([]byte{0x11}, 32)
	f2 := bytes.Repeat([]byte{0x22}, 32)
	blob := storedBlob(f1, f2, 1757862000)
	if len(blob) != 76 {
		t.Fatalf("precondition: synthetic blob must be 76 bytes like the real ones, got %d", len(blob))
	}

	got, err := Candidates(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !bytes.Equal(got[0], f1) || !bytes.Equal(got[1], f2) {
		t.Fatalf("candidates: got %x", got)
	}
}

func TestCandidatesRejectsBrokenBlobs(t *testing.T) {
	good := storedBlob(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), 1)
	for name, blob := range map[string][]byte{
		"empty":           nil,
		"cut in field 1":  good[:20],
		"length overflow": {0x0a, 0x7f, 0x01},
		"no 32-byte data": {0x18, 0x05},
	} {
		if _, err := Candidates(blob); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestDecryptRoundTrip(t *testing.T) {
	for _, size := range []int{0, 1, 15, 16, 17, 4096} {
		key := bytes.Repeat([]byte{0x42}, 32)
		plain := bytes.Repeat([]byte("wa"), size)[:size]
		body := encrypt(t, key, Video, plain)

		got, err := Decrypt(key, Video, body)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: plaintext mismatch", size)
		}
	}
}

func TestDecryptRejectsTampering(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	body := encrypt(t, key, Image, []byte("a photo that is longer than one block"))

	wrongKey := bytes.Repeat([]byte{0x43}, 32)
	if _, err := Decrypt(wrongKey, Image, body); !errors.Is(err, ErrBadMAC) {
		t.Errorf("wrong key: want ErrBadMAC, got %v", err)
	}
	if _, err := Decrypt(key, Document, body); !errors.Is(err, ErrBadMAC) {
		t.Errorf("wrong media type: want ErrBadMAC, got %v", err)
	}

	flipped := bytes.Clone(body)
	flipped[len(flipped)-1] ^= 0x01
	if _, err := Decrypt(key, Image, flipped); !errors.Is(err, ErrBadMAC) {
		t.Errorf("flipped MAC byte: want ErrBadMAC, got %v", err)
	}

	cut := body[:len(body)-17]
	if _, err := Decrypt(key, Image, cut); !errors.Is(err, ErrBadMAC) {
		t.Errorf("cut body: want ErrBadMAC, got %v", err)
	}

	if _, err := Decrypt(key, Image, body[:9]); !errors.Is(err, ErrShort) {
		t.Errorf("shorter than a MAC: want ErrShort, got %v", err)
	}
}

func TestOpenPicksTheFieldThatPassesHMAC(t *testing.T) {
	other := bytes.Repeat([]byte{0x99}, 32)
	key := bytes.Repeat([]byte{0x42}, 32)
	plain := []byte("voice note")
	body := encrypt(t, key, Audio, plain)

	got, field, err := Open(storedBlob(other, key, 7), Audio, body)
	if err != nil {
		t.Fatal(err)
	}
	if field != 2 || !bytes.Equal(got, plain) {
		t.Fatalf("got field %d plaintext %q", field, got)
	}

	if _, _, err := Open(storedBlob(other, other, 7), Audio, body); !errors.Is(err, ErrBadMAC) {
		t.Fatalf("no field holds the key: want ErrBadMAC, got %v", err)
	}
}

// storedBlob mirrors the layout measured in ZWAMEDIAITEM.ZMEDIAKEY on 2026-09-14:
// 0A 20 <32 bytes> 12 20 <32 bytes> 18 <varint>.
func storedBlob(f1, f2 []byte, ts uint64) []byte {
	b := []byte{0x0a, 0x20}
	b = append(b, f1...)
	b = append(b, 0x12, 0x20)
	b = append(b, f2...)
	b = append(b, 0x18)
	for {
		c := byte(ts & 0x7f)
		ts >>= 7
		if ts == 0 {
			b = append(b, c)
			break
		}
		b = append(b, c|0x80)
	}
	// Pad the varint to the measured 76-byte total with a second varint field.
	for len(b) < 76 {
		b = append(b, 0x20, 0x00)
	}
	return b[:76]
}

// encrypt is the sender side of the scheme, written independently of Decrypt.
func encrypt(t *testing.T, mediaKey []byte, typ Type, plain []byte) []byte {
	t.Helper()
	exp, err := hkdf.Key(sha256.New, mediaKey, nil, typ.info(), 112)
	if err != nil {
		t.Fatal(err)
	}
	iv, cipherKey, macKey := exp[:16], exp[16:48], exp[48:80]
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(bytes.Clone(plain), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(cipherKey)
	if err != nil {
		t.Fatal(err)
	}
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	m := hmac.New(sha256.New, macKey)
	m.Write(iv)
	m.Write(ct)
	return append(ct, m.Sum(nil)[:10]...)
}
