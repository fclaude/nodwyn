package rpc

import (
	"strings"
	"testing"
)

func TestProviderSetupHonoursInsecureTransportSetting(t *testing.T) {
	c, _, o, _ := withChat(t)
	provider := map[string]any{"name": "private", "provider": "openai", "url": "http://private.example.test/v1", "api_key": "fake-private-key"}
	if r := c.call("settings.provider.write", provider); r.Error == nil {
		t.Fatal("remote HTTP provider accepted with the setting off")
	}
	c.ok("settings.write", map[string]any{"set": map[string]any{"policy.allow_insecure_http": true}})
	var settings settingsResult
	into(t, c.ok("settings.provider.write", provider), &settings)
	if settings.Keychain.Credentials != 1 || len(settings.Servers) != 1 || settings.Servers[0].URL != provider["url"] {
		t.Fatalf("provider was not saved with its key: %+v", settings)
	}
	if strings.Contains(read(t, o.Paths.ConfigFile), provider["api_key"].(string)) {
		t.Fatal("the key was written to config.toml")
	}
	c.ok("settings.write", map[string]any{"set": map[string]any{"policy.allow_insecure_http": false}})
	delete(provider, "api_key")
	if r := c.call("settings.provider.write", provider); r.Error == nil {
		t.Fatal("existing credential ignored the disabled transport setting")
	}
}

func TestDefaultModelSetupHonoursInsecureTransportSetting(t *testing.T) {
	c, _, _, _ := withChat(t)
	model := map[string]any{"provider": "openai", "url": "http://private.example.test/v1", "model": "private-model", "api_key": "fake-private-key"}
	if r := c.call("settings.control.write", model); r.Error == nil {
		t.Fatal("keyed default accepted on remote HTTP with the setting off")
	}
	c.ok("settings.write", map[string]any{"set": map[string]any{"policy.allow_insecure_http": true}})
	var settings settingsResult
	into(t, c.ok("settings.control.write", model), &settings)
	if settings.Keychain.Credentials != 1 || len(settings.Servers) != 1 || settings.Servers[0].URL != model["url"] {
		t.Fatalf("default model was not saved with its key: %+v", settings)
	}
}
