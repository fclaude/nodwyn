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
//
// A credential has a kind (ADR 24): static, one value, or oauth2, a token
// set that expires and is refreshed here, inside the package, just before it
// is injected (oauth.go). Several processes may hold the same keychain open
// (the daemon, a CLI command); every change takes a lock on a sibling file
// and starts from what is on disk, and reads pick up a file another process
// wrote, so no process writes back a token another one already rotated.
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
	"syscall"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/fclaude/nodwyn/internal/scheme"
)

var (
	// The format marker keeps nodwyn's former name (ADR 31): renaming it
	// would make every existing keychain unreadable.
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

// The kinds of credential (ADR 24).
const (
	KindStatic = "static"
	KindOAuth2 = "oauth2"
)

// entry is one stored credential. value never appears in the handle that
// Scheme sees. A static entry holds its value; an oauth2 entry holds its
// token set, whose access token is what is injected.
type entry struct {
	class   string
	created time.Time
	value   value
	oauth   *tokenSet
}

func (e entry) kind() string {
	if e.oauth != nil {
		return KindOAuth2
	}
	return KindStatic
}

// wireDoc is the plaintext the AEAD protects: the metadata plus the base64
// values. It is never written to disk in this form. Version 1 holds only
// static keys; a keychain with an oauth2 entry is written as version 2, so
// a binary that predates them refuses it instead of rewriting it without
// its token sets.
type wireDoc struct {
	Version int     `json:"version"`
	Entries []wireE `json:"entries"`
}

type wireE struct {
	ID      string    `json:"id"`
	Class   string    `json:"class"`
	Created time.Time `json:"created"`
	Value   string    `json:"value,omitempty"` // base64, static
	Kind    string    `json:"kind,omitempty"`  // "" is static
	OAuth2  *tokenSet `json:"oauth2,omitempty"`
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
	stamp   stamp    // the file as this process last read or wrote it
	mu      sync.Mutex

	// client and now are the HTTP client and clock of OAuth2 refresh,
	// revocation and acquisition (ADR 24); tests replace them.
	client *http.Client
	now    func() time.Time
}

// stamp identifies a version of the keychain file, so a read can tell that
// another process has written it since.
type stamp struct {
	size int64
	mod  time.Time
}

func stampOf(path string) stamp {
	info, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{size: info.Size(), mod: info.ModTime()}
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
	st := openedStore(path, kdf, salt)
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

func openedStore(path string, kdf KDFParams, salt []byte) *Store {
	return &Store{path: path, kdf: kdf, salt: salt, entries: map[string]entry{}, client: defaultClient, now: time.Now}
}

// sealed is a keychain file split into its parts.
type sealed struct {
	kdf   KDFParams
	salt  []byte
	nonce []byte
	ct    []byte
}

func split(raw []byte) (sealed, error) {
	if len(raw) < headerLen+chacha20poly1305.NonceSizeX {
		return sealed{}, errors.New("keychain: file too short")
	}
	if !bytes.Equal(raw[:len(magic)], magic) {
		return sealed{}, errors.New("keychain: not a keychain file (bad magic)")
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
	return sealed{kdf: kdf, salt: salt, nonce: nonce, ct: raw[off:]}, nil
}

// Open reads and decrypts the keychain at path. A wrong passphrase is an
// AEAD authentication failure, reported as a distinct error.
func Open(path string, passphrase []byte) (*Store, error) {
	stamp := stampOf(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := split(raw)
	if err != nil {
		return nil, err
	}
	key := derive(f.kdf, passphrase, f.salt)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, f.nonce, f.ct, magic)
	if err != nil {
		return nil, errors.New("keychain: wrong passphrase or corrupted file")
	}
	st := openedStore(path, f.kdf, f.salt)
	st.aead = aead
	if err := st.decode(pt); err != nil {
		return nil, err
	}
	st.stamp = stamp
	return st, nil
}

// reloadLocked replaces the entries with what the file holds now. The
// caller holds st.mu.
func (st *Store) reloadLocked() error {
	stamp := stampOf(st.path)
	raw, err := os.ReadFile(st.path)
	if err != nil {
		return fmt.Errorf("keychain: reread: %w", err)
	}
	f, err := split(raw)
	if err != nil {
		return err
	}
	if !bytes.Equal(f.salt, st.salt) {
		return errors.New("keychain: the file was replaced by another keychain; unlock it again")
	}
	pt, err := st.aead.Open(nil, f.nonce, f.ct, magic)
	if err != nil {
		return errors.New("keychain: the file no longer opens with this passphrase; unlock it again")
	}
	entries, order := st.entries, st.order
	st.entries, st.order = map[string]entry{}, nil
	if err := st.decode(pt); err != nil {
		st.entries, st.order = entries, order
		return err
	}
	st.stamp = stamp
	return nil
}

// freshLocked rereads the file if another process wrote it since this one
// last did. A file that cannot be read keeps what is in memory. The caller
// holds st.mu.
func (st *Store) freshLocked() {
	if stampOf(st.path) != st.stamp {
		_ = st.reloadLocked()
	}
}

// change applies one change to the keychain as it is on disk: it takes the
// sibling lock shared by every nodwyn process, rereads the file, applies
// change and writes the result, so no process overwrites another's change.
func (st *Store) change(apply func() error) error {
	unlock, err := st.lock()
	if err != nil {
		return err
	}
	defer unlock()
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.reloadLocked(); err != nil {
		return err
	}
	if err := apply(); err != nil {
		return err
	}
	return st.save()
}

// lock takes the exclusive advisory lock on the keychain's sibling lock
// file. It serializes changes across processes, and refreshes across
// goroutines too, since each call opens its own descriptor.
func (st *Store) lock() (func(), error) {
	fd, err := syscall.Open(st.path+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("keychain: lock: %w", err)
	}
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("keychain: lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = syscall.Close(fd)
	}, nil
}

// Add stores a new credential of the given class and returns its opaque
// handle. It persists the store. The value is kept in memory, unexported,
// for the Inject* sinks; it is never returned.
func (st *Store) Add(class string, secret []byte) (*scheme.Handle, error) {
	if class == "" {
		return nil, errors.New("keychain: class is required")
	}
	return st.add(entry{class: class, created: time.Now().UTC(), value: value(append([]byte(nil), secret...))})
}

func (st *Store) add(e entry) (*scheme.Handle, error) {
	id := fmt.Sprintf("%x", random(8))
	err := st.change(func() error {
		if _, dup := st.entries[id]; dup {
			return errors.New("keychain: id collision")
		}
		st.entries[id] = e
		st.order = append(st.order, id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &scheme.Handle{Class: e.class, ID: id}, nil
}

// Delete removes a credential. An oauth2 credential's tokens stay valid at
// the provider; Revoke revokes them first.
func (st *Store) Delete(id string) error {
	return st.change(func() error {
		if _, ok := st.entries[id]; !ok {
			return errNotFound
		}
		delete(st.entries, id)
		for i, o := range st.order {
			if o == id {
				st.order = append(st.order[:i], st.order[i+1:]...)
				break
			}
		}
		return nil
	})
}

// Count returns the number of stored credentials.
func (st *Store) Count() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
	return len(st.entries)
}

// HandleForClass returns the first credential handle with the given class,
// or nil if none exists. Order is insertion order (deterministic).
func (st *Store) HandleForClass(class string) *scheme.Handle {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
	for _, id := range st.order {
		if st.entries[id].class == class {
			return &scheme.Handle{Class: class, ID: id}
		}
	}
	return nil
}

// Info is what may be said about a credential: everything but its secrets.
type Info struct {
	ID       string
	Class    string
	Kind     string // KindStatic or KindOAuth2
	Created  time.Time
	Provider string    // oauth2: the provider profile's name, or "custom"
	Scopes   []string  // oauth2: the scopes the token was granted
	Expires  time.Time // oauth2: when the access token expires; zero if it does not
	// Refreshable means an oauth2 token set has a refresh token.
	Refreshable bool
	// NeedsReauth means refreshing failed for good: every use is denied
	// until the user authorizes again (nodwyn credential login).
	NeedsReauth bool
	// TokenHost is the host of the oauth2 token endpoint refreshes go to.
	TokenHost string
}

func (e entry) info(id string) Info {
	in := Info{ID: id, Class: e.class, Kind: e.kind(), Created: e.created}
	if t := e.oauth; t != nil {
		in.Provider, in.Scopes, in.Expires = t.Provider, append([]string(nil), t.Scopes...), t.ExpiresAt
		in.Refreshable, in.NeedsReauth, in.TokenHost = t.RefreshToken != "", t.NeedsReauth, hostOf(t.TokenEndpoint)
	}
	return in
}

// Info describes the credential id.
func (st *Store) Info(id string) (Info, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
	e, ok := st.entries[id]
	if !ok {
		return Info{}, errNotFound
	}
	return e.info(id), nil
}

// List describes every credential, in the order they were added.
func (st *Store) List() []Info {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
	out := make([]Info, 0, len(st.order))
	for _, id := range st.order {
		out = append(out, st.entries[id].info(id))
	}
	return out
}

// Lookup returns the class and created time for id, or an error if absent.
func (st *Store) Lookup(id string) (class string, created time.Time, err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
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

// secret is what is injected for id: a static value, or an oauth2 access
// token that has not expired. An oauth2 token is made fresh by Refresh,
// which the caller runs first so the refresh is logged; one that expired
// anyway is refused, never sent stale.
func (st *Store) secret(id string) (value, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
	e, ok := st.entries[id]
	if !ok {
		return nil, errNotFound
	}
	if t := e.oauth; t != nil {
		if t.NeedsReauth {
			return nil, reauthError(id, e.class)
		}
		if !t.ExpiresAt.IsZero() && !st.now().Before(t.ExpiresAt) {
			return nil, fmt.Errorf("keychain: the %s access token expired and was not refreshed", e.class)
		}
		return value(t.AccessToken), nil
	}
	return e.value, nil
}

// save re-seals all entries and writes the file. The caller holds st.mu.
func (st *Store) save() error {
	doc := wireDoc{Version: 1}
	for _, id := range st.order {
		e := st.entries[id]
		we := wireE{ID: id, Class: e.class, Created: e.created}
		if e.oauth != nil {
			doc.Version = 2
			t := *e.oauth
			we.Kind, we.OAuth2 = KindOAuth2, &t
		} else {
			we.Value = base64.StdEncoding.EncodeToString(e.value)
		}
		doc.Entries = append(doc.Entries, we)
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
	st.stamp = stampOf(st.path)
	return nil
}

func (st *Store) decode(pt []byte) error {
	var doc wireDoc
	if err := json.Unmarshal(pt, &doc); err != nil {
		return fmt.Errorf("keychain: decode: %w", err)
	}
	if doc.Version != 1 && doc.Version != 2 {
		return fmt.Errorf("keychain: unsupported version %d", doc.Version)
	}
	for _, we := range doc.Entries {
		e := entry{class: we.Class, created: we.Created}
		switch we.Kind {
		case "", KindStatic:
			v, err := base64.StdEncoding.DecodeString(we.Value)
			if err != nil {
				return fmt.Errorf("keychain: value for %s: %w", we.ID, err)
			}
			e.value = value(v)
		case KindOAuth2:
			if we.OAuth2 == nil {
				return fmt.Errorf("keychain: oauth2 credential %s has no token set", we.ID)
			}
			t := *we.OAuth2
			e.oauth = &t
		default:
			return fmt.Errorf("keychain: credential %s has unknown kind %q", we.ID, we.Kind)
		}
		st.entries[we.ID] = e
		st.order = append(st.order, we.ID)
	}
	return nil
}
