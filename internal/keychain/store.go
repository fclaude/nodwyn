// Package keychain is the encrypted credential store (ADR 5). Secrets are
// credentials, not content (SPEC invariant 3): the harness holds opaque
// handles, and the value lives here, behind an AEAD. A value is never a
// scheme.Value and never leaves this package except through the Inject*
// sink functions, which write it into an outgoing request at a named
// position. The value type has no String() and no accessor at all.
//
// File format (all little-endian):
//
//	magic (8) | kdf: time(4) memory(4) threads(1) | salt (16) | nonce (24) | ciphertext
//
// The KDF parameters are in the cleartext header because they are needed to
// derive the key that opens the ciphertext. The AEAD is XChaCha20-Poly1305
// (24-byte nonce, 16-byte tag); the key is argon2id of the passphrase and
// salt. Losing the passphrase loses the credentials; they are re-registerable
// (ADR 5).
package keychain

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/fclaude/nibb/internal/scheme"
)

var (
	magic       = []byte("NIBBKC01")
	errNotFound = errors.New("keychain: no such credential")
)

const (
	keyLen    = 32
	saltLen   = 16
	headerLen = 8 + 4 + 4 + 1 + saltLen // magic + KDF params + salt
)

// value is the opaque secret. It is unexported and has no methods that yield
// the bytes; only the Inject* sink functions read it (ADR 5).
type value []byte

// entry is one stored credential. value never appears in the handle that
// Scheme sees.
type entry struct {
	class   string
	created time.Time
	value   value
}

// wireDoc is the plaintext the AEAD protects: the metadata plus the base64
// values. It is never written to disk in this form.
type wireDoc struct {
	Version int     `json:"version"`
	Entries []wireE `json:"entries"`
}

type wireE struct {
	ID      string    `json:"id"`
	Class   string    `json:"class"`
	Created time.Time `json:"created"`
	Value   string    `json:"value"` // base64
}

// Store is an opened keychain. The AEAD and KDF parameters are kept so
// entries can be re-sealed when one is added; the passphrase itself is not
// retained.
type Store struct {
	path    string
	aead    cipher.AEAD
	kdf     KDFParams
	salt    []byte
	entries map[string]entry
	order   []string // insertion order, for deterministic sealing
	mu      sync.Mutex
}

// KDFParams are the argon2id parameters used to derive the keychain key.
type KDFParams struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
}

// DefaultKDF is the production parameter set (OWASP-recommended memory-hard
// values for passphrase-based KDFs).
var DefaultKDF = KDFParams{Time: 2, Memory: 19 * 1024, Threads: 1}

// FastKDF is a low-cost parameter set for tests: milliseconds, not seconds.
// It is NOT for production.
var FastKDF = KDFParams{Time: 1, Memory: 8 * 1024, Threads: 1}

func derive(p KDFParams, passphrase, salt []byte) []byte {
	return argon2.IDKey(passphrase, salt, p.Time, p.Memory, p.Threads, keyLen)
}

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failed: not recoverable
	}
	return b
}

// Create makes a new, empty keychain at path using the given KDF parameters
// (pass DefaultKDF for production, FastKDF in tests).
func Create(path string, kdf KDFParams, passphrase []byte) (*Store, error) {
	salt := random(saltLen)
	st := &Store{path: path, kdf: kdf, salt: salt, entries: map[string]entry{}}
	aead, err := chacha20poly1305.NewX(derive(kdf, passphrase, salt))
	if err != nil {
		return nil, err
	}
	st.aead = aead
	if err := st.save(); err != nil {
		return nil, err
	}
	return st, nil
}

// Open reads and decrypts the keychain at path. A wrong passphrase is an
// AEAD authentication failure, reported as a distinct error.
func Open(path string, passphrase []byte) (*Store, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < headerLen+chacha20poly1305.NonceSizeX {
		return nil, errors.New("keychain: file too short")
	}
	if !bytes.Equal(raw[:len(magic)], magic) {
		return nil, errors.New("keychain: not a keychain file (bad magic)")
	}
	off := len(magic)
	kdf := KDFParams{
		Time:    binary.LittleEndian.Uint32(raw[off : off+4]),
		Memory:  binary.LittleEndian.Uint32(raw[off+4 : off+8]),
		Threads: raw[off+8],
	}
	off += 9
	salt := raw[off : off+saltLen]
	off += saltLen
	nonce := raw[off : off+chacha20poly1305.NonceSizeX]
	off += chacha20poly1305.NonceSizeX
	ct := raw[off:]

	key := derive(kdf, passphrase, salt)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, ct, magic)
	if err != nil {
		return nil, errors.New("keychain: wrong passphrase or corrupted file")
	}
	st := &Store{path: path, aead: aead, kdf: kdf, salt: salt, entries: map[string]entry{}}
	if err := st.decode(pt); err != nil {
		return nil, err
	}
	return st, nil
}

// Add stores a new credential of the given class and returns its opaque
// handle. It persists the store. The value is kept in memory, unexported,
// for the Inject* sinks; it is never returned.
func (st *Store) Add(class string, secret []byte) (*scheme.Handle, error) {
	if class == "" {
		return nil, errors.New("keychain: class is required")
	}
	id := fmt.Sprintf("%x", random(8))
	st.mu.Lock()
	if _, dup := st.entries[id]; dup {
		st.mu.Unlock()
		return nil, errors.New("keychain: id collision")
	}
	e := entry{class: class, created: time.Now().UTC(), value: value(append([]byte(nil), secret...))}
	st.entries[id] = e
	st.order = append(st.order, id)
	err := st.save()
	st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &scheme.Handle{Class: class, ID: id}, nil
}

// Count returns the number of stored credentials.
func (st *Store) Count() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.entries)
}

// HandleForClass returns the first credential handle with the given class,
// or nil if none exists. Order is insertion order (deterministic).
func (st *Store) HandleForClass(class string) *scheme.Handle {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, id := range st.order {
		if st.entries[id].class == class {
			return &scheme.Handle{Class: class, ID: id}
		}
	}
	return nil
}

// Lookup returns the class and created time for id, or an error if absent.
func (st *Store) Lookup(id string) (class string, created time.Time, err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e, ok := st.entries[id]
	if !ok {
		return "", time.Time{}, errNotFound
	}
	return e.class, e.created, nil
}

// Inject* are the only ways a value leaves the package: each writes the
// secret for id into an outgoing request at a named position (ADR 5).

// InjectHeader sets the request header name to prefix + value. prefix is
// the auth scheme with its trailing space ("Bearer "), or "" for a bare
// header such as x-api-key.
func (st *Store) InjectHeader(id, name, prefix string, req *http.Request) error {
	v, err := st.secret(id)
	if err != nil {
		return err
	}
	req.Header.Set(name, prefix+string(v))
	return nil
}

// InjectQuery sets the value as a URL query parameter key.
func (st *Store) InjectQuery(id, key string, req *http.Request) error {
	v, err := st.secret(id)
	if err != nil {
		return err
	}
	q := req.URL.Query()
	q.Set(key, string(v))
	req.URL.RawQuery = q.Encode()
	return nil
}

// InjectBody makes the value the request body.
func (st *Store) InjectBody(id string, req *http.Request) error {
	v, err := st.secret(id)
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(v))
	req.ContentLength = int64(len(v))
	return nil
}

func (st *Store) secret(id string) (value, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e, ok := st.entries[id]
	if !ok {
		return nil, errNotFound
	}
	return e.value, nil
}

// save re-seals all entries and writes the file.
func (st *Store) save() error {
	doc := wireDoc{Version: 1}
	for _, id := range st.order {
		e := st.entries[id]
		doc.Entries = append(doc.Entries, wireE{
			ID: id, Class: e.class, Created: e.created, Value: base64.StdEncoding.EncodeToString(e.value),
		})
	}
	pt, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	nonce := random(chacha20poly1305.NonceSizeX)
	ct := st.aead.Seal(nil, nonce, pt, magic)

	buf := make([]byte, 0, len(magic)+9+saltLen+len(nonce)+len(ct))
	buf = append(buf, magic...)
	var t [4]byte
	binary.LittleEndian.PutUint32(t[:], st.kdf.Time)
	buf = append(buf, t[:]...)
	binary.LittleEndian.PutUint32(t[:], st.kdf.Memory)
	buf = append(buf, t[:]...)
	buf = append(buf, st.kdf.Threads)
	buf = append(buf, st.salt...)
	buf = append(buf, nonce...)
	buf = append(buf, ct...)
	// Write-then-rename so a crash never leaves a half-written keychain.
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return fmt.Errorf("keychain: write: %w", err)
	}
	if err := os.Rename(tmp, st.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("keychain: write: %w", err)
	}
	return nil
}

func (st *Store) decode(pt []byte) error {
	var doc wireDoc
	if err := json.Unmarshal(pt, &doc); err != nil {
		return fmt.Errorf("keychain: decode: %w", err)
	}
	if doc.Version != 1 {
		return fmt.Errorf("keychain: unsupported version %d", doc.Version)
	}
	for _, we := range doc.Entries {
		v, err := base64.StdEncoding.DecodeString(we.Value)
		if err != nil {
			return fmt.Errorf("keychain: value for %s: %w", we.ID, err)
		}
		st.entries[we.ID] = entry{class: we.Class, created: we.Created, value: value(v)}
		st.order = append(st.order, we.ID)
	}
	return nil
}
