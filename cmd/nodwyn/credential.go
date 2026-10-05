package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/keychain"
)

// credential manages the keychain's credentials from the command line
// (ADR 24): list them, authorize an OAuth2 one with its provider, authorize
// it again, and revoke one. Authorizing is a user action, here and nowhere
// else: no primitive and no tool can put a consent screen in front of the
// user. A static key is still registered inside a session, at the masked
// prompt: :eval (credential-register "CLASS").
//
// A running daemon sees what these commands change: the keychain is one
// file, and every process rereads it when another has written it.
func (c *cli) credential() error {
	if err := c.rejectGlobals("credential: put %s after the command"); err != nil {
		return err
	}
	args := c.rest()
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch sub {
	case "list":
		return c.credentialList(args)
	case "add":
		return c.credentialAdd(ctx, args)
	case "login":
		return c.credentialLogin(ctx, args)
	case "revoke":
		return c.credentialRevoke(ctx, args)
	}
	return fmt.Errorf("credential: expected list, add, login or revoke")
}

// openStore opens the keychain for a credential command, which needs it.
func (c *cli) openStore() (*keychain.Store, error) {
	if _, err := os.Stat(c.paths.Keychain); err != nil {
		return nil, fmt.Errorf("no keychain at %s: create one with `nodwyn keychain init`", c.paths.Keychain)
	}
	store, _, err := openKeychain(c.paths, true)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("credential: the keychain passphrase needs a terminal")
	}
	return store, nil
}

func (c *cli) credentialList(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("credential list takes no arguments")
	}
	store, err := c.openStore()
	if err != nil {
		return err
	}
	list := store.List()
	if len(list) == 0 {
		fmt.Println("The keychain holds no credentials.")
	}
	for _, in := range list {
		fmt.Printf("%s  %-20s %-7s added %s\n", in.ID, in.Class, in.Kind, in.Created.Local().Format("2006-01-02"))
		if in.Kind != keychain.KindOAuth2 {
			continue
		}
		fmt.Printf("    provider %s, tokens from %s\n", in.Provider, in.TokenHost)
		fmt.Printf("    scopes   %s\n", orNone(strings.Join(in.Scopes, " ")))
		switch {
		case in.NeedsReauth:
			fmt.Printf("    needs authorizing again: nodwyn credential login %s\n", in.ID)
		case in.Expires.IsZero():
			fmt.Println("    the access token does not expire")
		case in.Refreshable:
			fmt.Printf("    access token until %s, refreshed before use\n", in.Expires.Local().Format("2006-01-02 15:04"))
		default:
			fmt.Printf("    access token until %s; no refresh token, so authorize again after that\n", in.Expires.Local().Format("2006-01-02 15:04"))
		}
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func (c *cli) credentialAdd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("credential add", flag.ContinueOnError)
	provider := fs.String("oauth2", "", "the provider: "+strings.Join(keychain.ProfileNames(), ", ")+", or custom")
	class := fs.String("class", "", "the credential class bindings name (default: the provider)")
	scopes := fs.String("scopes", "", "comma-separated scopes (default: the provider's narrow set)")
	clientID := fs.String("client-id", "", "the OAuth client id you registered with the provider")
	secret := fs.Bool("client-secret", false, "ask for the client's secret (a confidential client, or a provider that requires one)")
	authURL := fs.String("auth-url", "", "custom provider: the authorization endpoint")
	tokenURL := fs.String("token-url", "", "custom provider: the token endpoint")
	revokeURL := fs.String("revoke-url", "", "custom provider: the revocation endpoint, if it has one")
	noBrowser := fs.Bool("no-browser", false, "print the authorization URL instead of opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("credential add: unexpected %q", fs.Arg(0))
	}
	if *provider == "" {
		return errors.New("credential add: only --oauth2 PROVIDER is added here; a static key is registered in a session: :eval (credential-register \"CLASS\")")
	}
	var profile keychain.Profile
	if *provider == "custom" {
		if *authURL == "" || *tokenURL == "" {
			return errors.New("credential add: a custom provider needs --auth-url and --token-url")
		}
		profile = keychain.Custom(*authURL, *tokenURL, *revokeURL)
	} else {
		var ok bool
		if profile, ok = keychain.ProfileNamed(*provider); !ok {
			return fmt.Errorf("credential add: no provider %q (%s, or custom)", *provider, strings.Join(keychain.ProfileNames(), ", "))
		}
		if *authURL != "" || *tokenURL != "" || *revokeURL != "" {
			return errors.New("credential add: --auth-url, --token-url and --revoke-url are for --oauth2 custom")
		}
	}
	if *class == "" {
		*class = profile.Name
	}
	g := keychain.Grant{Profile: profile, ClientID: *clientID}
	if *scopes != "" {
		g.Scopes = strings.Split(*scopes, ",")
	}
	store, err := c.openStore()
	if err != nil {
		return err
	}
	if *secret {
		s, err := readPassphrase("client secret: ")
		if err != nil {
			return err
		}
		g.ClientSecret = string(s)
	}
	h, err := store.AddOAuth2(ctx, *class, g, showAuthorization(ctx, profile.Name, *noBrowser))
	if err != nil {
		return err
	}
	info, err := store.Info(h.ID)
	if err != nil {
		return err
	}
	fmt.Printf("Stored %s (%s), granted: %s\n", info.Class, info.ID, orNone(strings.Join(info.Scopes, " ")))
	fmt.Printf("Nothing uses it until a binding names it. In %s:\n\n", c.paths.Bindings)
	fmt.Printf("[[bind]]\nclass = %q\ndest = %q\npositions = [\"header:Authorization:Bearer\"]\nmethods = [\"GET\", \"POST\"]\n", info.Class, apiHost(profile))
	if len(info.Scopes) > 0 {
		fmt.Printf("scopes = [%s]\n", quoted(info.Scopes))
	}
	fmt.Println()
	if profile.Name == "openai" {
		fmt.Println("Use this credential with the Responses API. In " + c.paths.ConfigFile + ":")
		fmt.Printf("\n[servers.chatgpt]\nprovider = \"openai\"\nurl = \"https://api.openai.com/v1\"\ndialect = \"responses\"\ncredential = %q\n", info.Class)
		fmt.Println("\nChoose a model available to this ChatGPT account; quota and plan eligibility are enforced by OpenAI.")
	}
	return nil
}

// apiHost is a binding hint: where a provider's tokens are usually sent.
func apiHost(p keychain.Profile) string {
	if p.APIHost == "" {
		return "the API's host"
	}
	return p.APIHost
}

func quoted(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}

// showAuthorization prints the authorization URL and opens it in the
// default browser, unless asked not to.
func showAuthorization(ctx context.Context, provider string, noBrowser bool) func(string) error {
	return func(url string) error {
		fmt.Fprintf(os.Stderr, "Authorize nodwyn with %s at:\n\n  %s\n\n", provider, url)
		if !noBrowser {
			if err := openBrowser(ctx, url); err != nil {
				fmt.Fprintln(os.Stderr, "(no browser opened:", err.Error()+"; open the address yourself)")
			}
		}
		fmt.Fprintf(os.Stderr, "Waiting up to 5 minutes for the provider to send you back to nodwyn...\n")
		return nil
	}
}

// resolve finds a credential by id, or by class when one has it.
func resolve(store *keychain.Store, ref string) (keychain.Info, error) {
	if in, err := store.Info(ref); err == nil {
		return in, nil
	}
	if h := store.HandleForClass(ref); h != nil {
		return store.Info(h.ID)
	}
	return keychain.Info{}, fmt.Errorf("no credential with id or class %q (nodwyn credential list)", ref)
}

func (c *cli) credentialLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("credential login", flag.ContinueOnError)
	noBrowser := fs.Bool("no-browser", false, "print the authorization URL instead of opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: nodwyn credential login [--no-browser] ID|CLASS")
	}
	store, err := c.openStore()
	if err != nil {
		return err
	}
	in, err := resolve(store, fs.Arg(0))
	if err != nil {
		return err
	}
	if err := store.Reauthorize(ctx, in.ID, showAuthorization(ctx, in.Provider, *noBrowser)); err != nil {
		return err
	}
	if in, err = store.Info(in.ID); err != nil {
		return err
	}
	until := "does not expire"
	if !in.Expires.IsZero() {
		until = "valid until " + in.Expires.Local().Format(time.DateTime)
	}
	fmt.Printf("%s (%s) is authorized again; the access token %s.\n", in.Class, in.ID, until)
	return nil
}

func (c *cli) credentialRevoke(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("credential revoke", flag.ContinueOnError)
	local := fs.Bool("local", false, "only delete it here, without asking the provider to revoke it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: nodwyn credential revoke [--local] ID|CLASS")
	}
	store, err := c.openStore()
	if err != nil {
		return err
	}
	in, err := resolve(store, fs.Arg(0))
	if err != nil {
		return err
	}
	if *local {
		if err := store.Delete(in.ID); err != nil {
			return err
		}
		fmt.Printf("Deleted %s (%s) here; it is still valid at its service.\n", in.Class, in.ID)
		return nil
	}
	revoked, err := store.Revoke(ctx, in.ID)
	if err != nil {
		return err
	}
	switch {
	case revoked:
		fmt.Printf("Revoked %s (%s) at %s and deleted it here.\n", in.Class, in.ID, in.Provider)
	case in.Kind == keychain.KindOAuth2:
		fmt.Printf("Deleted %s (%s) here. %s has no revocation endpoint: its tokens stay valid until they expire, or until you remove nodwyn in your %s account's settings.\n", in.Class, in.ID, in.Provider, in.Provider)
	default:
		fmt.Printf("Deleted %s (%s) here. nodwyn cannot revoke a static key: revoke it where it was issued.\n", in.Class, in.ID)
	}
	return nil
}
