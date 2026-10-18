package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// Devices remembers the browsers paired from other machines (ADR 30), the
// passkeys their person signs in with and the invitations not yet used
// (amended 2026-10-17). A token or an invitation's code is stored only as
// its SHA-256 and a passkey only as its public key, so the file cannot pair
// a browser.
//
// The bridge and nodwyn devices write it, as the user, in the config
// directory (0700). A task's file tools never reach that directory
// (prim.Options.Private), and a command a person approved already runs as
// them, so whoever can write the file can do what it grants without it.
type Devices struct {
	path string
	mu   sync.Mutex
}

// Device is one paired browser.
type Device struct {
	Name     string    `toml:"name" json:"name"`
	Hash     string    `toml:"token_sha256" json:"-"`
	Paired   time.Time `toml:"paired" json:"paired"`
	LastSeen time.Time `toml:"last_seen" json:"last_seen"`
	// Passkey names the passkey this device's person last proved themself
	// with. A device with one is the person's, and makes this computer's
	// choices; one paired by a plain invitation has none. Check leaves it
	// empty once that passkey is removed.
	Passkey string `toml:"passkey,omitempty" json:"passkey,omitempty"`
	// By is who let it in: the device that invited it, the passkey it
	// signed in with, or the terminal.
	By string `toml:"by,omitempty" json:"by,omitempty"`
}

// Passkey is a passkey (WebAuthn) the person registered with this console,
// named after the device that registered it.
type Passkey struct {
	Name      string    `toml:"name" json:"name"`
	ID        string    `toml:"id" json:"-"`         // the credential id, base64url
	PublicKey string    `toml:"public_key" json:"-"` // SubjectPublicKeyInfo DER, base64url
	Added     time.Time `toml:"added" json:"added"`
	By        string    `toml:"by" json:"by"`
	LastUsed  time.Time `toml:"last_used" json:"last_used"`
}

// invite is an invitation not yet used. One that may register a passkey
// comes from the terminal or from a device whose person touched theirs; a
// plain one pairs a device that only answers.
type invite struct {
	Hash    string    `toml:"code_sha256"`
	Expires time.Time `toml:"expires"`
	By      string    `toml:"by"`
	Passkey bool      `toml:"passkey"`
}

type deviceFile struct {
	Device  []Device  `toml:"device"`
	Passkey []Passkey `toml:"passkey"`
	Invite  []invite  `toml:"invite"`
}

// DeviceName is what a device may be called: it becomes part of the actor
// recorded with every answer it gives (web:NAME).
var DeviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// idleLife is how long a device stays signed in without being used. Use
// renews it: its console's every load and connection renews the cookie, and
// Check refuses only a device not seen for this long. A device in use is
// never signed out by a timer (ADR 30, amended 2026-10-17).
const idleLife = 90 * 24 * time.Hour

// inviteLife is how long an invitation waits to be used.
const inviteLife = 10 * time.Minute

// Terminal is who an invitation made by nodwyn devices invite is by.
const Terminal = "terminal"

// OpenDevices uses path (devices.toml in the config directory).
func OpenDevices(path string) *Devices { return &Devices{path: path} }

func (d *Devices) load() (deviceFile, error) {
	var f deviceFile
	_, err := toml.DecodeFile(d.path, &f)
	if errors.Is(err, os.ErrNotExist) {
		return deviceFile{}, nil
	}
	return f, err
}

func (d *Devices) save(f deviceFile) error {
	dir := filepath.Dir(d.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".devices-*")
	if err != nil {
		return err
	}
	_, _ = tmp.WriteString("# Browsers paired with nodwyn from other machines, and the passkeys they sign in with. Revoke with: nodwyn devices revoke NAME\n")
	werr := toml.NewEncoder(tmp).Encode(f)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o600)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), d.path)
}

// change loads the file, lets fn change it and saves it, under the lock.
func (d *Devices) change(fn func(f *deviceFile) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, err := d.load()
	if err != nil {
		return err
	}
	if err := fn(&f); err != nil {
		return err
	}
	return d.save(f)
}

func digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// pair records a device under name in f, replacing one of the same name,
// and returns the token its cookie carries.
func pair(f *deviceFile, name, by, passkey string) (string, error) {
	if !DeviceName.MatchString(name) {
		return "", fmt.Errorf("name the device with lower-case letters, digits, dots or dashes (at most 32)")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	f.Device = slices.DeleteFunc(f.Device, func(x Device) bool { return x.Name == name })
	now := time.Now().UTC().Truncate(time.Second)
	f.Device = append(f.Device, Device{Name: name, Hash: digest(token), Paired: now, LastSeen: now, Passkey: passkey, By: by})
	return token, nil
}

// Check returns the device token belongs to. The file is read on every
// check, so a revocation applies at once: to new requests, and to an open
// console at its next request or recheck. A device not seen for idleLife is
// forgotten instead.
func (d *Devices) Check(token string) (Device, bool) {
	if token == "" {
		return Device{}, false
	}
	want := digest(token)
	d.mu.Lock()
	defer d.mu.Unlock()
	f, err := d.load()
	if err != nil {
		return Device{}, false
	}
	for i, x := range f.Device {
		if subtle.ConstantTimeCompare([]byte(x.Hash), []byte(want)) != 1 {
			continue
		}
		now := time.Now().UTC()
		if now.Sub(x.LastSeen) > idleLife {
			f.Device = slices.Delete(f.Device, i, i+1)
			_ = d.save(f)
			return Device{}, false
		}
		// Last seen is kept to the hour: enough for the idle limit and to
		// spot a stale device, without rewriting the file on every request.
		if now.Sub(x.LastSeen) > time.Hour {
			f.Device[i].LastSeen = now.Truncate(time.Second)
			_ = d.save(f)
		}
		if !slices.ContainsFunc(f.Passkey, func(p Passkey) bool { return p.Name == x.Passkey }) {
			x.Passkey = ""
		}
		return x, true
	}
	return Device{}, false
}

// List returns the paired devices.
func (d *Devices) List() ([]Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, err := d.load()
	return f.Device, err
}

// Revoke forgets a device; its cookie stops working at its next request.
func (d *Devices) Revoke(name string) error {
	return d.change(func(f *deviceFile) error {
		kept := slices.DeleteFunc(slices.Clone(f.Device), func(x Device) bool { return x.Name == name })
		if len(kept) == len(f.Device) {
			return fmt.Errorf("no paired device is named %q", name)
		}
		f.Device = kept
		return nil
	})
}

// codeAlphabet spells an invitation's code: lower case, without the letters
// most often misread (i, l, o, u), since a code may be typed on a phone.
const codeAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// normalCode is a typed code as it was made: lower case, without the dashes
// and spaces it is shown with.
func normalCode(code string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		case r == '-' || r == ' ':
			return -1
		}
		return r
	}, code)
}

// Invite makes an invitation by by, good once within inviteLife, and returns
// its code: 20 characters (100 bits) in groups of four. With passkey, the
// device that uses it may register a passkey.
func (d *Devices) Invite(by string, passkey bool) (string, time.Time, error) {
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, err
	}
	var code strings.Builder
	for i, b := range raw {
		if i > 0 && i%4 == 0 {
			code.WriteByte('-')
		}
		code.WriteByte(codeAlphabet[int(b)%len(codeAlphabet)])
	}
	expires := time.Now().UTC().Add(inviteLife).Truncate(time.Second)
	err := d.change(func(f *deviceFile) error {
		now := time.Now()
		f.Invite = slices.DeleteFunc(f.Invite, func(x invite) bool { return now.After(x.Expires) })
		f.Invite = append(f.Invite, invite{Hash: digest(normalCode(code.String())), Expires: expires, By: by, Passkey: passkey})
		return nil
	})
	return code.String(), expires, err
}

var errInvite = errors.New("that invitation was already used, has expired or was mistyped: ask for another")

// take removes the live invitation with code from f and returns it.
func take(f *deviceFile, code string) (invite, error) {
	want := digest(normalCode(code))
	now := time.Now()
	for i, x := range f.Invite {
		if subtle.ConstantTimeCompare([]byte(x.Hash), []byte(want)) == 1 {
			f.Invite = slices.Delete(f.Invite, i, i+1)
			if now.After(x.Expires) {
				return invite{}, errInvite
			}
			return x, nil
		}
	}
	return invite{}, errInvite
}

// Invited reports whether code is a live invitation, and whether it may
// register a passkey, without using it.
func (d *Devices) Invited(code string) (ok, passkey bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, err := d.load()
	if err != nil {
		return false, false
	}
	x, err := take(&f, code)
	return err == nil, x.Passkey
}

// Join uses an invitation to pair a device named name, which only answers,
// and returns its token.
func (d *Devices) Join(code, name string) (string, error) {
	var token string
	err := d.change(func(f *deviceFile) error {
		x, err := take(f, code)
		if err != nil {
			return err
		}
		token, err = pair(f, name, x.By, "")
		return err
	})
	return token, err
}

// Register uses an invitation that may register a passkey: the passkey key
// (named after the device, replacing one of that name) and the device
// signed in with it. It returns the device's token.
func (d *Devices) Register(code, name string, key Passkey) (string, error) {
	var token string
	err := d.change(func(f *deviceFile) error {
		x, err := take(f, code)
		if err != nil {
			return err
		}
		if !x.Passkey {
			return errors.New("this invitation pairs a device that answers, without a passkey")
		}
		if token, err = pair(f, name, x.By, name); err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Second)
		key.Name, key.By, key.Added, key.LastUsed = name, x.By, now, now
		f.Passkey = append(slices.DeleteFunc(f.Passkey, func(p Passkey) bool { return p.Name == name || p.ID == key.ID }), key)
		return nil
	})
	return token, err
}

// Passkeys returns the registered passkeys.
func (d *Devices) Passkeys() ([]Passkey, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, err := d.load()
	return f.Passkey, err
}

// used marks passkey as just used in f.
func used(f *deviceFile, passkey string) error {
	for i := range f.Passkey {
		if f.Passkey[i].Name == passkey {
			f.Passkey[i].LastUsed = time.Now().UTC().Truncate(time.Second)
			return nil
		}
	}
	return fmt.Errorf("no passkey is named %q", passkey)
}

// SignIn pairs a device named name whose person just proved themself with
// passkey, and returns its token.
func (d *Devices) SignIn(name, passkey string) (string, error) {
	var token string
	err := d.change(func(f *deviceFile) error {
		if err := used(f, passkey); err != nil {
			return err
		}
		var err error
		token, err = pair(f, name, "passkey:"+passkey, passkey)
		return err
	})
	return token, err
}

// Touched records that device's person just proved themself with passkey:
// a device paired without one is the person's from now on.
func (d *Devices) Touched(device, passkey string) error {
	return d.change(func(f *deviceFile) error {
		if err := used(f, passkey); err != nil {
			return err
		}
		for i := range f.Device {
			if f.Device[i].Name == device {
				f.Device[i].Passkey = passkey
				return nil
			}
		}
		return fmt.Errorf("no paired device is named %q", device)
	})
}

// RemovePasskey forgets a passkey, such as one on a phone that was lost:
// it signs nothing in from then on, and the devices it signed in only
// answer. The last one stays, since without it only the terminal could let
// the person in again: register another first.
func (d *Devices) RemovePasskey(name string) error {
	return d.change(func(f *deviceFile) error {
		kept := slices.DeleteFunc(slices.Clone(f.Passkey), func(p Passkey) bool { return p.Name == name })
		switch {
		case len(kept) == len(f.Passkey):
			return fmt.Errorf("no passkey is named %q", name)
		case len(kept) == 0:
			return errors.New("this is the only passkey: invite a device to register another first, or nodwyn devices invite on the server lets you in again")
		}
		f.Passkey = kept
		return nil
	})
}
