package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// tableHeader matches a TOML table or array-of-tables header line.
var tableHeader = regexp.MustCompile(`^\s*\[\[?\s*([A-Za-z0-9_."-]+)\s*\]\]?\s*(#.*)?$`)

// SetControl returns raw (config.toml's contents) with its [control] table
// replaced by m, keeping every other line — comments included — as written.
// A file without [control] gains one at the end. The result is parsed and
// validated before it is returned, so a caller never writes a file nodwyn
// would refuse to load. A model that names a server is written with the
// server's name, not the provider, dialect, URL and credential Parse copied from it.
func SetControl(raw []byte, m Model) ([]byte, Config, error) {
	return setTable(raw, "control", modelBlock("control", m))
}

// SetModel returns raw with its [models.NAME] table replaced by m, as
// SetControl does for [control]: the models a person can choose between are
// these tables (ADR 41). The name is what (model-turn "NAME") asks.
func SetModel(raw []byte, name string, m Model) ([]byte, Config, error) {
	if !keyName.MatchString(name) {
		return nil, Config{}, fmt.Errorf("config: %q is not a model name: use letters, digits, - and _", name)
	}
	return setTable(raw, "models."+name, modelBlock("models."+name, m))
}

// RemoveModel returns raw without its [models.NAME] table, and every other
// line as it was. A name config.toml does not have changes nothing.
func RemoveModel(raw []byte, name string) ([]byte, Config, error) {
	if !keyName.MatchString(name) {
		return nil, Config{}, fmt.Errorf("config: %q is not a model name", name)
	}
	return setTable(raw, "models."+name, "")
}

// RemoveServer returns raw without its [servers.NAME] table. The result is
// parsed as every edit's is, so a server a model still names is refused by
// the loader's own words.
func RemoveServer(raw []byte, name string) ([]byte, Config, error) {
	if !keyName.MatchString(name) {
		return nil, Config{}, fmt.Errorf("config: %q is not a server name", name)
	}
	return setTable(raw, "servers."+name, "")
}

// SetServer returns raw with its [servers.NAME] table replaced by s, as
// SetControl does for [control]. The models that name the server reach it
// at its new address and with its new key.
func SetServer(raw []byte, name string, s Server) ([]byte, Config, error) {
	if !keyName.MatchString(name) {
		return nil, Config{}, fmt.Errorf("config: %q is not a server name: use letters, digits, - and _", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[servers.%s]\n", name)
	for _, f := range []struct{ key, value string }{{"provider", s.Provider}, {"dialect", string(s.Dialect)}, {"url", s.URL}, {"credential", s.Credential}} {
		if f.value != "" {
			fmt.Fprintf(&b, "%s = %s\n", f.key, quote(f.value))
		}
	}
	b.WriteString("\n")
	return setTable(raw, "servers."+name, b.String())
}

// setTable replaces table, and any table under it, with block; an empty
// block removes it. The comments and blank lines after the table's last
// setting belong to the table that follows, as Edit reads them, and stay.
func setTable(raw []byte, table, block string) ([]byte, Config, error) {
	lines := strings.SplitAfter(string(raw), "\n")
	var out strings.Builder
	replaced, skipping := false, false
	var held []string // comments and blank lines met while skipping
	for _, line := range lines {
		if match := tableHeader.FindStringSubmatch(strings.TrimRight(line, "\r\n")); match != nil {
			name := strings.Trim(match[1], `"`)
			if name == table || strings.HasPrefix(name, table+".") {
				if !replaced {
					out.WriteString(block)
					replaced = true
				}
				skipping, held = true, nil
				continue
			}
			if skipping {
				// The next table's own lines: what came before its header
				// after the last setting, its leading blank lines dropped,
				// since the block ends with one.
				for len(held) > 0 && strings.TrimSpace(held[0]) == "" {
					held = held[1:]
				}
				for _, h := range held {
					out.WriteString(h)
				}
				skipping, held = false, nil
			}
		}
		if skipping {
			if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
				held = append(held, line)
			} else {
				held = nil // a setting: what was held was the table's own
			}
			continue
		}
		out.WriteString(line)
	}
	text := out.String()
	if !replaced && block != "" {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if text != "" {
			text += "\n"
		}
		text += block
	}
	cfg, err := Parse([]byte(text))
	if err != nil {
		return nil, Config{}, err
	}
	return []byte(text), cfg, nil
}

// SetKey returns raw (config.toml's contents) with key in table set to the
// string value, as Edit does.
func SetKey(raw []byte, table, key, value string) ([]byte, Config, error) {
	return Edit(raw, Change{Table: table, Key: key, Value: value})
}

// A Change sets Key in Table to Value: a string, bool, int64 or float64. A
// nil Value removes the key, so its default applies.
type Change struct {
	Table, Key string
	Value      any
}

var (
	tableName = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)
	keyName   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// Edit returns raw (config.toml's contents) with changes made in order,
// keeping every other line, comments included, as written: an existing
// assignment is replaced or removed in place, a missing one is added at the
// end of its table, and a missing table at the end of the file. Only the
// result is parsed and validated, so changes that are valid together (a
// soft limit raised past the old hard one, with the hard one) may be made
// at once; a caller never writes a file nodwyn would refuse to load.
func Edit(raw []byte, changes ...Change) ([]byte, Config, error) {
	text := string(raw)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	lines := strings.SplitAfter(text, "\n")
	lines = lines[:len(lines)-1] // the empty string after the final newline
	for _, c := range changes {
		var err error
		if lines, err = edit(lines, c); err != nil {
			return nil, Config{}, err
		}
	}
	text = strings.Join(lines, "")
	cfg, err := Parse([]byte(text))
	if err != nil {
		return nil, Config{}, err
	}
	return []byte(text), cfg, nil
}

func edit(lines []string, c Change) ([]string, error) {
	if !tableName.MatchString(c.Table) || !keyName.MatchString(c.Key) {
		return nil, fmt.Errorf("config: no setting %s.%s", c.Table, c.Key)
	}
	var line string
	if c.Value != nil {
		value, err := literal(c.Value)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", c.Table, c.Key, err)
		}
		line = fmt.Sprintf("%s = %s\n", c.Key, value)
	}
	assign := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(c.Key) + `\s*=`)
	in, found, at := false, false, -1
	for i, l := range lines {
		if match := tableHeader.FindStringSubmatch(strings.TrimRight(l, "\r\n")); match != nil {
			in = strings.Trim(match[1], `"`) == c.Table
			if in {
				found, at = true, i+1
			}
			continue
		}
		if !in {
			continue
		}
		if assign.MatchString(l) {
			if line == "" {
				return append(lines[:i:i], lines[i+1:]...), nil
			}
			lines[i] = line
			return lines, nil
		}
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			at = i + 1 // after the table's last setting
		}
	}
	switch {
	case line == "": // nothing to remove
	case found:
		lines = append(lines[:at:at], append([]string{line}, lines[at:]...)...)
	default:
		if len(lines) > 0 {
			lines = append(lines, "\n")
		}
		lines = append(lines, "["+c.Table+"]\n", line)
	}
	return lines, nil
}

// literal writes a TOML value. A float always has a decimal point, so it
// reads back as a float.
func literal(v any) (string, error) {
	switch v := v.(type) {
	case string:
		return quote(v), nil
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return "", fmt.Errorf("%v is not a number TOML can hold", v)
		}
		s := strconv.FormatFloat(v, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s, nil
	}
	return "", fmt.Errorf("a %T is not a setting's value", v)
}

// Keys reports which settings config.toml's contents set, by dotted name
// such as "budget.tokens_hard".
func Keys(raw []byte) (map[string]bool, error) {
	var c Config
	md, err := toml.Decode(string(raw), &c)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, k := range md.Keys() {
		set[k.String()] = true
	}
	return set, nil
}

// Value is setting key of table in c, as config.toml spells it: a string,
// bool, int64 or float64, or nil for an optional setting left unset (such as
// control.temperature). ok is false when c has no such setting.
func (c Config) Value(table, key string) (value any, ok bool) {
	v := reflect.ValueOf(c)
	for _, name := range append(strings.Split(table, "."), key) {
		if v.Kind() != reflect.Struct {
			return nil, false
		}
		field, found := tomlField(v, name)
		if !found {
			return nil, false
		}
		v = field
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, true
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.String:
		return v.String(), true
	case reflect.Bool:
		return v.Bool(), true
	case reflect.Int, reflect.Int64:
		return v.Int(), true
	case reflect.Float64:
		return v.Float(), true
	}
	return nil, false
}

func tomlField(v reflect.Value, name string) (reflect.Value, bool) {
	for i := range v.NumField() {
		if tag, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("toml"), ","); tag == name {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// modelBlock is the table named table (control, or models.NAME) holding m.
func modelBlock(table string, m Model) string {
	var b strings.Builder
	b.WriteString("[" + table + "]\n")
	field := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s = %s\n", name, quote(value))
		}
	}
	if m.Server != "" {
		field("server", m.Server)
	} else {
		field("provider", m.Provider)
		field("dialect", string(m.Dialect))
		field("url", m.URL)
	}
	field("model", m.Model)
	if m.NumCtx > 0 {
		fmt.Fprintf(&b, "num_ctx = %d\n", m.NumCtx)
	}
	field("keep_alive", m.KeepAlive)
	if m.Server == "" {
		field("credential", m.Credential)
	}
	if m.MaxOutput > 0 {
		fmt.Fprintf(&b, "max_output = %d\n", m.MaxOutput)
	}
	field("output_field", m.OutputField)
	if m.Temperature != nil {
		fmt.Fprintf(&b, "temperature = %s\n", strconv.FormatFloat(*m.Temperature, 'f', -1, 64))
	}
	if m.Seed != nil {
		fmt.Fprintf(&b, "seed = %d\n", *m.Seed)
	}
	if m.Vision != nil {
		fmt.Fprintf(&b, "vision = %t\n", *m.Vision)
	}
	field("cache_key", m.CacheKey)
	field("cache_control", m.CacheControl)
	if m.KeepReasoning {
		b.WriteString("keep_reasoning = true\n")
	}
	price := func(name string, p *float64) {
		if p != nil {
			fmt.Fprintf(&b, "%s = %s\n", name, strconv.FormatFloat(*p, 'f', -1, 64))
		}
	}
	price("price_input", m.PriceInput)
	price("price_cached_input", m.PriceCachedInput)
	price("price_output", m.PriceOutput)
	if len(m.Efforts) > 0 {
		quoted := make([]string, len(m.Efforts))
		for i, e := range m.Efforts {
			quoted[i] = quote(e)
		}
		fmt.Fprintf(&b, "efforts = [%s]\n", strings.Join(quoted, ", "))
	}
	field("effort", m.Effort)
	if len(m.Labels) > 0 {
		quoted := make([]string, len(m.Labels))
		for i, l := range m.Labels {
			quoted[i] = quote(l)
		}
		fmt.Fprintf(&b, "labels = [%s]\n", strings.Join(quoted, ", "))
	}
	b.WriteString("\n")
	return b.String()
}

// quote writes a TOML basic string. JSON's escapes are a subset of TOML's.
func quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// WriteFile replaces path with raw atomically, mode 0600, creating its
// directory privately. The configuration holds no secret, but it names
// where content goes, so it is the user's alone.
func WriteFile(path string, raw []byte) error {
	if !filepath.IsAbs(path) {
		return errors.New("config: path must be absolute")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("config: %s is not a regular file", path)
	}
	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(raw)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr, os.Chmod(tmp.Name(), 0o600)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
