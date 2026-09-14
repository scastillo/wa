// Package mediakey decrypts WhatsApp media with the key stored in ZWAMEDIAITEM.ZMEDIAKEY.
//
// The scheme (verified against whatsmeow download.go on 2026-09-14):
//   - HKDF-SHA256 with an empty salt expands the 32-byte media key to 112 bytes:
//     IV [0:16], AES key [16:48], MAC key [48:80];
//   - the CDN body is AES-256-CBC ciphertext followed by 10 MAC bytes;
//   - the MAC is HMAC-SHA256(MAC key, IV || ciphertext) cut to 10 bytes.
//
// ZMEDIAKEY is not the raw key. It is a small protobuf with two 32-byte fields and
// a varint (measured on real data). Open tries each 32-byte field; only the real
// key can pass the MAC check, so the MAC check also identifies the field.
package mediakey

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Type selects the HKDF info string.
type Type int

const (
	Image Type = iota + 1
	Video
	Audio
	Document
	Sticker
)

const (
	keyLen     = 32
	macLen     = 10
	expandSize = 112
)

var (
	ErrBadMAC  = errors.New("media MAC check failed")
	ErrShort   = errors.New("media body shorter than its MAC")
	ErrPadding = errors.New("media padding is invalid")
)

func (t Type) String() string {
	switch t {
	case Image:
		return "image"
	case Video:
		return "video"
	case Audio:
		return "audio"
	case Document:
		return "document"
	case Sticker:
		return "sticker"
	}
	return fmt.Sprintf("type(%d)", int(t))
}

func (t Type) info() string {
	switch t {
	case Image, Sticker:
		return "WhatsApp Image Keys"
	case Video:
		return "WhatsApp Video Keys"
	case Audio:
		return "WhatsApp Audio Keys"
	case Document:
		return "WhatsApp Document Keys"
	}
	return ""
}

func expand(key []byte, info string, n int) ([]byte, error) {
	return hkdf.Key(sha256.New, key, nil, info, n)
}

// Candidates returns every 32-byte bytes field of a stored ZMEDIAKEY blob, in order.
func Candidates(blob []byte) ([][]byte, error) {
	var out [][]byte
	for i := 0; i < len(blob); {
		tag, n := binary.Uvarint(blob[i:])
		if n <= 0 {
			return nil, fmt.Errorf("media key: bad field tag at byte %d", i)
		}
		i += n
		switch tag & 7 {
		case 0:
			if _, n := binary.Uvarint(blob[i:]); n <= 0 {
				return nil, fmt.Errorf("media key: bad varint at byte %d", i)
			} else {
				i += n
			}
		case 2:
			l, n := binary.Uvarint(blob[i:])
			if n <= 0 {
				return nil, fmt.Errorf("media key: bad length at byte %d", i)
			}
			i += n
			if l > uint64(len(blob)-i) {
				return nil, fmt.Errorf("media key: field of %d bytes overruns the blob at byte %d", l, i)
			}
			if l == keyLen {
				out = append(out, blob[i:i+keyLen])
			}
			i += int(l)
		default:
			return nil, fmt.Errorf("media key: unsupported wire type %d at byte %d", tag&7, i)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("media key: no 32-byte field")
	}
	return out, nil
}

// Decrypt checks the MAC of a CDN body, then decrypts it.
func Decrypt(mediaKey []byte, t Type, body []byte) ([]byte, error) {
	if len(mediaKey) != keyLen {
		return nil, fmt.Errorf("media key: want %d bytes, got %d", keyLen, len(mediaKey))
	}
	info := t.info()
	if info == "" {
		return nil, fmt.Errorf("media key: no key info for %v", t)
	}
	if len(body) < macLen {
		return nil, ErrShort
	}
	exp, err := expand(mediaKey, info, expandSize)
	if err != nil {
		return nil, err
	}
	iv, cipherKey, macKey := exp[:16], exp[16:48], exp[48:80]
	ct, mac := body[:len(body)-macLen], body[len(body)-macLen:]

	m := hmac.New(sha256.New, macKey)
	m.Write(iv)
	m.Write(ct)
	if !hmac.Equal(m.Sum(nil)[:macLen], mac) {
		return nil, ErrBadMAC
	}

	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, ErrPadding
	}
	block, err := aes.NewCipher(cipherKey)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)
	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize || !bytes.Equal(plain[len(plain)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, ErrPadding
	}
	return plain[:len(plain)-pad], nil
}

// Open decrypts a CDN body with the stored ZMEDIAKEY blob. It returns the plaintext
// and the 1-based position of the 32-byte field that held the key.
func Open(blob []byte, t Type, body []byte) ([]byte, int, error) {
	cands, err := Candidates(blob)
	if err != nil {
		return nil, 0, err
	}
	for i, key := range cands {
		plain, err := Decrypt(key, t, body)
		if err == nil {
			return plain, i + 1, nil
		}
		if !errors.Is(err, ErrBadMAC) {
			return nil, 0, err
		}
	}
	return nil, 0, ErrBadMAC
}
