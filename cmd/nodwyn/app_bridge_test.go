package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDesktopBridgeChecksNativeOriginAndMethods(t *testing.T) {
	const origin = "http://127.0.0.1:12345"
	var opened string
	picked, notified := 0, 0
	b := desktopBridge{origin: origin, token: "test-capability",
		pickFolder:   func() (string, error) { picked++; return "/tmp/a'\"</script>", nil },
		openExternal: func(link string) error { opened = link; return nil },
		notify:       func() error { notified++; return nil },
	}
	for _, source := range []struct {
		origin, top string
		main        bool
	}{
		{"https://example.com", origin, true}, {origin, "https://example.com", true}, {origin, origin, false},
	} {
		b.handle(source.origin, source.top, source.main, `{"id":1,"method":"pickFolder","token":"test-capability"}`, func(string) { t.Fatal("untrusted frame received a reply") })
	}
	if picked != 0 {
		t.Fatal("untrusted frame opened a dialog")
	}
	for _, message := range []string{
		`{"id":1,"method":"pickFolder"}`, `{"id":1,"method":"pickFolder","token":"wrong"}`,
		`{"id":0,"method":"pickFolder","token":"test-capability"}`, strings.Repeat("x", 17000),
	} {
		b.handle(origin+"/", "", false, message, func(string) { t.Fatal("invalid capability or request received a reply") })
	}
	if picked != 0 {
		t.Fatal("invalid capability opened a dialog")
	}
	// GTK supplies the top URL instead of frame metadata. Only a holder of the
	// injected capability may use that path.
	b.handle(origin+"/", "", false, `{"id":1,"method":"pickFolder","token":"test-capability"}`, func(string) {})
	if picked != 1 {
		t.Fatal("GTK request with capability was not handled")
	}
	picked = 0
	call := func(method, link string) string {
		t.Helper()
		message, err := json.Marshal(map[string]any{"id": 1, "method": method, "url": link, "token": "test-capability"})
		if err != nil {
			t.Fatal(err)
		}
		var reply string
		b.handle(origin, origin, true, string(message), func(js string) { reply = js })
		if !strings.HasPrefix(reply, `if(location.origin === "`+origin+`")`) {
			t.Fatalf("unguarded reply: %s", reply)
		}
		return reply
	}
	if reply := call("pickFolder", ""); picked != 1 || strings.Contains(reply, "</script>") {
		t.Fatalf("dialog reply: %s", reply)
	}
	for _, link := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://user:pass@example.com"} {
		if reply := call("openExternal", link); !strings.Contains(reply, `"error"`) || opened != "" {
			t.Fatalf("opened unsafe link: %s", reply)
		}
	}
	call("openExternal", "https://example.com/help")
	if opened != "https://example.com/help" {
		t.Fatal("link was not opened")
	}
	call("notify", "")
	if notified != 1 {
		t.Fatal("notification was not sent")
	}
	if reply := call("shell", ""); !strings.Contains(reply, "unknown desktop method") {
		t.Fatal(reply)
	}
}

func TestDesktopOriginRequiresLoopback(t *testing.T) {
	for _, address := range []string{"https://example.com", "file:///tmp/console", "http://localhost:1234", "http://127.0.0.1", "http://user@127.0.0.1:1234"} {
		if _, err := desktopOrigin(address); err == nil {
			t.Fatalf("accepted %s", address)
		}
	}
	if origin, err := desktopOrigin("http://127.0.0.1:1234/#ticket=secret"); err != nil || origin != "http://127.0.0.1:1234" {
		t.Fatalf("%s: %v", origin, err)
	}
}
