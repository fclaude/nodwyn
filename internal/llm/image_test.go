package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

var picture = Image{MediaType: "image/png", SHA256: "0011", Data: []byte("\x89PNG\r\n\x1a\npixels")}

func TestOllamaSendsImagesAsBase64(t *testing.T) {
	srv := fake.New(fake.Turn{Content: "a cat"})
	defer srv.Close()
	o := NewOllama(srv.URL)
	_, err := o.Chat(context.Background(), Request{Model: "m", Messages: []Message{
		{Role: RoleUser, Content: "[Attached image]", Images: []Image{picture}},
		{Role: RoleUser, Content: "what is it?"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	for _, raw := range srv.Requests[0].Messages {
		var m map[string]any
		json.Unmarshal(raw, &m)
		msgs = append(msgs, m)
	}
	images, _ := msgs[0]["images"].([]any)
	if len(images) != 1 || images[0] != base64.StdEncoding.EncodeToString(picture.Data) || msgs[0]["content"] != "[Attached image]" {
		t.Fatalf("image message: %v", msgs[0])
	}
	if _, ok := msgs[1]["images"]; ok {
		t.Fatalf("a message without images carries some: %v", msgs[1])
	}
	if strings.Contains(string(srv.Requests[0].Raw), "sha256") || strings.Contains(string(srv.Requests[0].Raw), "media_type") {
		t.Fatalf("the provider-neutral fields went over the wire: %s", srv.Requests[0].Raw)
	}
}

func TestOllamaVisionFromShow(t *testing.T) {
	srv := fake.New()
	defer srv.Close()
	o := NewOllama(srv.URL)
	if v, err := o.Vision(context.Background(), "m"); err != nil || v {
		t.Fatalf("a model without vision: %v %v", v, err)
	}
	srv.SetCapabilities("completion", "vision")
	if v, err := o.Vision(context.Background(), "m"); err != nil || !v {
		t.Fatalf("a vision model: %v %v", v, err)
	}
	down := httptest.NewServer(http.NotFoundHandler())
	defer down.Close()
	if _, err := NewOllama(down.URL).Vision(context.Background(), "m"); err == nil {
		t.Fatal("a failed show reported a capability")
	}
}

// A chat-completions message with images is an array of parts: its text,
// then each image as a data URL.
func TestOpenAISendsImagesAsContentParts(t *testing.T) {
	var got struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":[{"type":"text","text":"a "},{"type":"text","text":"cat"}]},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL+"/v1", nil)
	resp, err := o.Chat(context.Background(), Request{Model: "m", Messages: []Message{
		{Role: RoleUser, Content: "[Attached image]", Images: []Image{picture}},
		{Role: RoleUser, Content: "what is it?"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"text","text":"[Attached image]"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(picture.Data) + `"}}]`
	if string(got.Messages[0].Content) != want {
		t.Fatalf("image message content:\n%s\nwant\n%s", got.Messages[0].Content, want)
	}
	if string(got.Messages[1].Content) != `"what is it?"` {
		t.Fatalf("a text message is no longer a string: %s", got.Messages[1].Content)
	}
	if resp.Message.Content != "a cat" {
		t.Fatalf("parts in a reply: %q", resp.Message.Content)
	}
	if v, _ := o.Vision(context.Background(), "m"); v {
		t.Fatal("vision without the setting")
	}
	o.Images = true
	if v, _ := o.Vision(context.Background(), "m"); !v {
		t.Fatal("vision = true not reported")
	}
}

// A reply that is only tool calls may carry a null content.
func TestOpenAINullContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"fs-read","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer srv.Close()
	resp, err := NewOpenAI(srv.URL, nil).Chat(context.Background(), Request{Model: "m"}, nil)
	if err != nil || resp.Message.Content != "" || len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("%+v %v", resp, err)
	}
}
