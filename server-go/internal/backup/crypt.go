package backup

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// Backup artifacts are encrypted in the backup CronJob with
//
//	openssl enc -aes-256-cbc -pbkdf2 -iter 100000 -md sha256 -salt -pass env:BACKUP_ENCRYPTION_KEY
//
// because openssl is the one tool the backup image can carry without a
// custom binary. This file reads and writes that exact format so the
// server can decrypt a download and tests can prove compatibility.
// Integrity comes from the manifest's sha256, which is taken over the
// plaintext: without the key, a tampered ciphertext cannot be made to
// decrypt to a plaintext matching it.
const (
	EncryptionAlg = "openssl-aes-256-cbc-pbkdf2-sha256"
	PBKDF2Iter    = 100000
	saltMagic     = "Salted__"
)

// KeySecretKey is the data key inside the per-addon key Secret.
const KeySecretKey = "key"

// KeySecretName is the per-addon Secret holding the backup encryption key,
// rendered by the kusoaddon chart (kusoaddon.backupKeySecretName) with
// helm.sh/resource-policy keep.
func KeySecretName(addonCR string) string { return addonCR + "-backup-key" }

// ErrEncryptedNoKey is returned when an artifact is encrypted and no key
// was supplied.
var ErrEncryptedNoKey = errors.New("backup artifact is encrypted but no encryption key is available")

// KeyID is the short public fingerprint of an encryption key recorded in
// manifests, so a restore can tell which key a dump needs without the
// key itself ever being written to the bucket.
func KeyID(passphrase string) string {
	sum := sha256.Sum256([]byte("kuso-backup-key:" + passphrase))
	return hex.EncodeToString(sum[:])[:16]
}

// IsEncrypted reports whether head (the first bytes of an artifact) is
// openssl's salted header. Legacy dumps start with the gzip magic.
func IsEncrypted(head []byte) bool {
	return len(head) >= len(saltMagic) && string(head[:len(saltMagic)]) == saltMagic
}

func deriveKeyIV(passphrase string, salt []byte) (key, iv []byte, err error) {
	kiv, err := pbkdf2.Key(sha256.New, passphrase, salt, PBKDF2Iter, 32+aes.BlockSize)
	if err != nil {
		return nil, nil, fmt.Errorf("derive backup key: %w", err)
	}
	return kiv[:32], kiv[32:], nil
}

// Encrypt writes r to w in the openssl format above.
func Encrypt(w io.Writer, r io.Reader, passphrase string) error {
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("salt: %w", err)
	}
	key, iv, err := deriveKeyIV(passphrase, salt)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("cipher: %w", err)
	}
	enc := cipher.NewCBCEncrypter(block, iv)
	if _, err := w.Write(append([]byte(saltMagic), salt...)); err != nil {
		return err
	}
	buf := make([]byte, 64*1024)
	var pending []byte
	for {
		n, rerr := r.Read(buf)
		pending = append(pending, buf[:n]...)
		if full := len(pending) / aes.BlockSize * aes.BlockSize; full > 0 && rerr == nil {
			// Hold back the tail so the final block can be padded.
			if full == len(pending) {
				full -= aes.BlockSize
			}
			out := make([]byte, full)
			enc.CryptBlocks(out, pending[:full])
			if _, err := w.Write(out); err != nil {
				return err
			}
			pending = append(pending[:0], pending[full:]...)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	pad := aes.BlockSize - len(pending)%aes.BlockSize
	pending = append(pending, bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(pending))
	enc.CryptBlocks(out, pending)
	_, err = w.Write(out)
	return err
}

// MaybeDecrypt returns a reader over the plaintext of an artifact. A
// legacy plaintext artifact passes through unchanged; an encrypted one
// needs passphrase, else ErrEncryptedNoKey.
func MaybeDecrypt(r io.Reader, passphrase string) (io.Reader, bool, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	head, err := br.Peek(len(saltMagic))
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, false, err
	}
	if !IsEncrypted(head) {
		return br, false, nil
	}
	if passphrase == "" {
		return nil, true, ErrEncryptedNoKey
	}
	dr, err := NewDecryptReader(br, passphrase)
	return dr, true, err
}

type decryptReader struct {
	src  io.Reader
	dec  cipher.BlockMode
	buf  []byte // undecrypted ciphertext
	out  []byte // decrypted, ready to hand out
	last []byte // final decrypted block, held until EOF for unpadding
	eof  bool
}

// NewDecryptReader decrypts an artifact that IsEncrypted, header included.
func NewDecryptReader(r io.Reader, passphrase string) (io.Reader, error) {
	hdr := make([]byte, len(saltMagic)+8)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("read encryption header: %w", err)
	}
	key, iv, err := deriveKeyIV(passphrase, hdr[len(saltMagic):])
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	return &decryptReader{src: r, dec: cipher.NewCBCDecrypter(block, iv)}, nil
}

func (d *decryptReader) Read(p []byte) (int, error) {
	for len(d.out) == 0 {
		if d.eof {
			return 0, io.EOF
		}
		chunk := make([]byte, 64*1024)
		n, err := d.src.Read(chunk)
		d.buf = append(d.buf, chunk[:n]...)
		if full := len(d.buf) / aes.BlockSize * aes.BlockSize; full > 0 {
			plain := make([]byte, full)
			d.dec.CryptBlocks(plain, d.buf[:full])
			d.buf = append(d.buf[:0], d.buf[full:]...)
			d.out = append(d.out, d.last...)
			d.out = append(d.out, plain[:full-aes.BlockSize]...)
			d.last = append([]byte(nil), plain[full-aes.BlockSize:]...)
		}
		if errors.Is(err, io.EOF) {
			if len(d.buf) != 0 || len(d.last) != aes.BlockSize {
				return 0, errors.New("backup decrypt: truncated ciphertext")
			}
			pad := int(d.last[aes.BlockSize-1])
			if pad == 0 || pad > aes.BlockSize || !bytes.Equal(d.last[aes.BlockSize-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
				return 0, errors.New("backup decrypt: bad padding (wrong key?)")
			}
			d.out = append(d.out, d.last[:aes.BlockSize-pad]...)
			d.last = nil
			d.eof = true
		} else if err != nil {
			return 0, err
		}
	}
	n := copy(p, d.out)
	d.out = d.out[n:]
	return n, nil
}
