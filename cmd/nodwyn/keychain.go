package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
)

// keychain creates the encrypted credential store ("keychain init") and
// sets, changes or removes its passphrase ("keychain passphrase"). Its
// credentials are managed with "credential" (credential.go) or in the
// console, and static keys are also registered from inside a session.
func (c *cli) keychain() error {
	if err := c.rejectGlobals("keychain: put --%s after the command"); err != nil {
		return err
	}
	args := c.rest()
	if len(args) > 0 {
		switch args[0] {
		case "init":
			return c.keychainInit(args[1:])
		case "passphrase":
			return c.keychainPassphrase(args[1:])
		}
	}
	return fmt.Errorf("keychain: expected init or passphrase")
}

// noPassphraseWarning is said whenever a keychain is left without a
// passphrase: what it gives up.
const noPassphraseWarning = "it has no passphrase: anyone who can read the file, or a backup of it, can use every key in it"

func (c *cli) keychainInit(args []string) error {
	fs := flag.NewFlagSet("keychain init", flag.ContinueOnError)
	none := fs.Bool("no-passphrase", false, "create it without a passphrase (not recommended): it opens without asking, and the file is as safe as its permissions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("keychain init: unexpected %q", fs.Arg(0))
	}
	if _, err := os.Stat(c.paths.Keychain); err == nil {
		return fmt.Errorf("keychain already exists at %s", c.paths.Keychain)
	}
	var pass []byte
	if !*none {
		var err error
		if pass, err = newPassphrase("new keychain passphrase: "); err != nil {
			return err
		}
		if len(pass) == 0 {
			return fmt.Errorf("empty passphrase (nodwyn keychain init --no-passphrase makes a keychain without one)")
		}
	}
	if err := os.MkdirAll(filepath.Dir(c.paths.Keychain), 0o700); err != nil {
		return err
	}
	if _, err := keychain.Create(c.paths.Keychain, keychain.DefaultKDF, pass); err != nil {
		return err
	}
	fmt.Println("keychain created at", c.paths.Keychain)
	if *none {
		fmt.Printf("%s; nodwyn keychain passphrase sets one.\n", strings.ToUpper(noPassphraseWarning[:1])+noPassphraseWarning[1:])
	}
	return nil
}

// keychainPassphrase sets, changes or removes the passphrase. Changing one
// asks for the current passphrase first: whoever removes it can read every
// key from the file.
func (c *cli) keychainPassphrase(args []string) error {
	fs := flag.NewFlagSet("keychain passphrase", flag.ContinueOnError)
	remove := fs.Bool("remove", false, "remove the passphrase (not recommended): the keychain then opens without asking")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("keychain passphrase: unexpected %q", fs.Arg(0))
	}
	if _, err := os.Stat(c.paths.Keychain); err != nil {
		return fmt.Errorf("no keychain at %s: create one with `nodwyn keychain init`", c.paths.Keychain)
	}
	store, err := c.openStore()
	if err != nil {
		return err
	}
	if *remove {
		if !store.HasPassphrase() {
			return fmt.Errorf("the keychain has no passphrase")
		}
		if err := store.SetPassphrase(nil); err != nil {
			return err
		}
		fmt.Printf("Removed the passphrase; %s.\n", noPassphraseWarning)
		return nil
	}
	pass, err := newPassphrase("new keychain passphrase: ")
	if err != nil {
		return err
	}
	defer clear(pass)
	if len(pass) == 0 {
		return fmt.Errorf("empty passphrase (--remove removes it)")
	}
	had := store.HasPassphrase()
	if err := store.SetPassphrase(pass); err != nil {
		return err
	}
	if had {
		fmt.Println("Changed the keychain passphrase. A daemon that has it unlocked keeps using its keys, and asks again when it next starts.")
	} else {
		fmt.Println("The keychain now has a passphrase. A daemon that has it open keeps using its keys, and asks for it when it next starts.")
	}
	return nil
}

// newPassphrase reads a new passphrase twice.
func newPassphrase(prompt string) ([]byte, error) {
	pass, err := readPassphrase(prompt)
	if err != nil {
		return nil, err
	}
	again, err := readPassphrase("again: ")
	if err != nil {
		return nil, err
	}
	defer clear(again)
	if string(pass) != string(again) {
		clear(pass)
		return nil, fmt.Errorf("passphrases do not match")
	}
	return pass, nil
}

// readPassphrase reads a masked line from the terminal. Passphrases never
// come from the environment or from arguments, which other processes can
// read (ADR 5).
func readPassphrase(prompt string) ([]byte, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("a passphrase needs a terminal")
	}
	return readPassphraseFrom(os.Stdin, os.Stderr, prompt)
}

func readPassphraseFrom(tty *os.File, out io.Writer, prompt string) ([]byte, error) {
	fmt.Fprint(out, prompt)
	secret, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(out)
	return secret, err
}

// openKeychain opens the store if one exists: at once if it has no
// passphrase; else if stdin is a terminal, or, with controllingTTY, when
// the process has a controlling terminal (/dev/tty) — as ssh and sudo ask —
// so a session whose input is a script can still be unlocked by the person
// who started it.
// Without either the session simply has no credential primitives, which is
// the bootstrap posture, so a missing keychain is not an error.
func openKeychain(paths config.Paths, controllingTTY bool) (*keychain.Store, binding.Table, error) {
	if _, err := os.Stat(paths.Keychain); err != nil {
		return nil, binding.Table{}, nil
	}
	if store, err := keychain.OpenWithoutPassphrase(paths.Keychain); err == nil {
		return opened(paths, store)
	} else if !errors.Is(err, keychain.ErrNeedsPassphrase) {
		return nil, binding.Table{}, err
	}
	tty, out := os.Stdin, io.Writer(os.Stderr)
	if !term.IsTerminal(int(tty.Fd())) {
		var err error
		tty = nil
		if controllingTTY {
			if tty, err = os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil && !term.IsTerminal(int(tty.Fd())) {
				tty.Close()
				tty = nil
			}
		}
		if tty == nil {
			fmt.Fprintln(os.Stderr, "keychain: not a terminal; starting without credentials (use --no-keychain to silence)")
			return nil, binding.Table{}, nil
		}
		defer tty.Close()
		out = tty // stderr may be a log; the prompt is for the person at the terminal
	}
	for attempt := 0; attempt < 3; attempt++ {
		pass, err := readPassphraseFrom(tty, out, "keychain passphrase: ")
		if err != nil {
			return nil, binding.Table{}, err
		}
		store, err := keychain.Open(paths.Keychain, pass)
		clear(pass)
		if err != nil {
			fmt.Fprintln(out, "keychain:", err)
			continue
		}
		return opened(paths, store)
	}
	return nil, binding.Table{}, fmt.Errorf("keychain: could not open")
}

// opened loads the binding table to go with an opened keychain, and says
// what was opened.
func opened(paths config.Paths, store *keychain.Store) (*keychain.Store, binding.Table, error) {
	table, err := binding.Load(paths.Bindings)
	if err != nil {
		return nil, binding.Table{}, err
	}
	how := ""
	if !store.HasPassphrase() {
		how = " (no passphrase)"
	}
	fmt.Fprintf(os.Stderr, "keychain: %d credential(s), %d binding row(s)%s\n", store.Count(), len(table.Bind), how)
	return store, table, nil
}
