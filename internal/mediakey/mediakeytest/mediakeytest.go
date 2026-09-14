// Package mediakeytest builds WhatsApp-style encrypted media for tests. It is the
// sender side of the scheme that package mediakey decrypts, written independently.
package mediakeytest

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"

	"github.com/scastillo/wa/internal/mediakey"
)

var info = map[mediakey.Type]string{
	mediakey.Image:    "WhatsApp Image Keys",
	mediakey.Sticker:  "WhatsApp Image Keys",
	mediakey.Video:    "WhatsApp Video Keys",
	mediakey.Audio:    "WhatsApp Audio Keys",
	mediakey.Document: "WhatsApp Document Keys",
}

// Encrypt returns the CDN body for plain: AES-256-CBC ciphertext plus a 10-byte MAC.
func Encrypt(mediaKey []byte, t mediakey.Type, plain []byte) ([]byte, error) {
	label, ok := info[t]
	if !ok {
		return nil, fmt.Errorf("no key info for %v", t)
	}
	exp, err := hkdf.Key(sha256.New, mediaKey, nil, label, 112)
	if err != nil {
		return nil, err
	}
	iv, cipherKey, macKey := exp[:16], exp[16:48], exp[48:80]
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(bytes.Clone(plain), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(cipherKey)
	if err != nil {
		return nil, err
	}
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	m := hmac.New(sha256.New, macKey)
	m.Write(iv)
	m.Write(ct)
	return append(ct, m.Sum(nil)[:10]...), nil
}

// Blob returns a stored ZMEDIAKEY value: the key in field 1, a second 32-byte field,
// and a varint, as measured on real data.
func Blob(mediaKey []byte) []byte {
	b := []byte{0x0a, 0x20}
	b = append(b, mediaKey...)
	b = append(b, 0x12, 0x20)
	b = append(b, bytes.Repeat([]byte{0xee}, 32)...)
	return append(b, 0x18, 0x01)
}
