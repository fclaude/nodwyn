package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	fake "github.com/fclaude/nodwyn/test/fakes/mcp"
)

// stdioPair runs serve on the far ends of two pipes and returns a client on
// the near ends. ended says why the far side stopped.
func stdioPair(t *testing.T, serve func(in io.Reader, out io.WriteCloser)) *Client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go serve(inR, outW)
	c := NewStdio(inW, outR, func() error { return errors.New("exit status 3") })
	t.Cleanup(func() { c.Close() })
	return c
}

func TestStdioClient(t *testing.T) {
	server := fake.NewStdio(fake.Tool{Name: "search", Description: "Search notes", Reply: func(args map[string]any) (string, bool) {
		return "found: " + args["q"].(string), false
	}})
	c := stdioPair(t, func(in io.Reader, out io.WriteCloser) {
		server.Serve(in, out)
		out.Close()
	})
	ctx := context.Background()
	info, err := c.Initialize(ctx)
	if err != nil || info.Name != "fake" {
		t.Fatalf("initialize: %+v %v", info, err)
	}
	tools, err := c.Tools(ctx)
	if err != nil || len(tools) != 1 || tools[0].Name != "search" {
		t.Fatalf("tools: %+v %v", tools, err)
	}
	for _, q := range []string{"one", "two"} {
		result, err := c.Call(ctx, "search", map[string]any{"q": q})
		if err != nil || result.Text != "found: "+q || result.IsError {
			t.Fatalf("call: %+v %v", result, err)
		}
	}
	if calls := server.Recorded(); len(calls) != 2 || calls[1].Args["q"] != "two" {
		t.Fatalf("calls: %+v", calls)
	}
	if c.Host() != "" {
		t.Fatal("a stdio server has a host")
	}
}

// A server that dies in the middle of a call fails the call, says why, and
// fails every later one at once.
func TestStdioServerExitsMidCall(t *testing.T) {
	c := stdioPair(t, func(in io.Reader, out io.WriteCloser) {
		lines := bufio.NewScanner(in)
		for lines.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			json.Unmarshal(lines.Bytes(), &req)
			switch req.Method {
			case "initialize":
				io.WriteString(out, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":{"serverInfo":{"name":"dying"}}}`+"\n")
			case "tools/call":
				out.Close() // gone before it answers
				return
			}
		}
	})
	ctx := context.Background()
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := c.Call(ctx, "anything", nil)
	if err == nil || !strings.Contains(err.Error(), "output ended") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("mid-call exit: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Tools(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a call after the exit succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a call after the exit hung")
	}
}

// The server's own requests are answered (ping) or refused (anything a
// client would need a capability for); notifications and noise are
// ignored; a cancelled call tells the server.
func TestStdioServerRequestsAndCancellation(t *testing.T) {
	replies := make(chan string, 4)
	cancelled := make(chan string, 1)
	c := stdioPair(t, func(in io.Reader, out io.WriteCloser) {
		lines := bufio.NewScanner(in)
		for lines.Scan() {
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			json.Unmarshal(lines.Bytes(), &msg)
			switch {
			case msg.Method == "initialize":
				io.WriteString(out, "not json at all\n")
				io.WriteString(out, `{"jsonrpc":"2.0","method":"notifications/message","params":{}}`+"\n")
				io.WriteString(out, `{"jsonrpc":"2.0","id":"s1","method":"ping"}`+"\n")
				io.WriteString(out, `{"jsonrpc":"2.0","id":"s2","method":"sampling/createMessage","params":{}}`+"\n")
				io.WriteString(out, `{"jsonrpc":"2.0","id":`+string(msg.ID)+`,"result":{"serverInfo":{"name":"chatty"}}}`+"\n")
			case msg.Method == "tools/list":
				// never answered
			case msg.Method == "notifications/cancelled":
				cancelled <- string(msg.Params)
			case msg.Method == "":
				replies <- lines.Text()
			}
		}
	})
	info, err := c.Initialize(context.Background())
	if err != nil || info.Name != "chatty" {
		t.Fatalf("initialize: %+v %v", info, err)
	}
	got := map[string]string{}
	for range 2 {
		var r struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		line := <-replies
		json.Unmarshal([]byte(line), &r)
		got[r.ID] = line
		if r.ID == "s1" && (string(r.Result) != "{}" || r.Error != nil) {
			t.Fatalf("ping: %s", line)
		}
		if r.ID == "s2" && (r.Error == nil || r.Error.Code != -32601) {
			t.Fatalf("sampling: %s", line)
		}
	}
	if len(got) != 2 {
		t.Fatalf("replies: %v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Tools(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unanswered: %v", err)
	}
	select {
	case params := <-cancelled:
		if !strings.Contains(params, `"requestId"`) {
			t.Fatalf("cancelled: %s", params)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server was not told the call was cancelled")
	}
}

// A line longer than any response may be ends the conversation.
func TestStdioMessageTooLong(t *testing.T) {
	c := stdioPair(t, func(in io.Reader, out io.WriteCloser) {
		bufio.NewReader(in).ReadString('\n')
		huge := strings.Repeat("x", maxBody+10)
		io.WriteString(out, `{"jsonrpc":"2.0","id":1,"result":"`+huge+`"}`+"\n")
	})
	if _, err := c.Initialize(context.Background()); err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("too long: %v", err)
	}
}
