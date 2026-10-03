package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// Images as attachments (ADR 22 §3, amended 2026-10-01), against a fake
// control model that does or does not take images.

// imageRig is a session whose control model is a fake ollama at 127.0.0.1,
// taking images when vision is set; adjust changes the options first.
func imageRig(t *testing.T, vision bool, adjust func(*Options), turns ...fake.Turn) *rig {
	t.Helper()
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	if vision {
		srv.SetCapabilities("completion", "tools", "vision")
	}
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "fake-vision"
	logb := &bytes.Buffer{}
	o := Options{Config: cfg, Repo: t.TempDir(), Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: logb}
	if adjust != nil {
		adjust(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: o.Repo, logb: logb}
}

// picture writes a w×h PNG named name and returns its path and bytes.
func picture(t *testing.T, name string, w, h int) (string, []byte) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, b.Bytes()
}

// attachAnswering attaches the file at path, answering its confirm, if one
// is asked, with answer; it returns the confirm it saw.
func (r *rig) attachAnswering(path, name, answer string) (log.Attach, *event.Confirm, error) {
	r.t.Helper()
	type result struct {
		receipt log.Attach
		err     error
	}
	done := make(chan result, 1)
	go func() {
		receipt, err := r.s.Attach(path, name, "unix:attacher")
		done <- result{receipt, err}
	}()
	var asked *event.Confirm
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-r.s.Events():
			if ev.Kind == event.PromptConfirm {
				c := ev.Payload.(event.Confirm)
				asked = &c
				if err := r.s.AnswerFrom(c.ID, answer, "unix:answerer"); err != nil {
					r.t.Fatal(err)
				}
			}
		case res := <-done:
			return res.receipt, asked, res.err
		case <-timeout:
			r.t.Fatal("the attachment did not finish")
		}
	}
}

// imagesIn returns the base64 images each message of a fake ollama request
// carries.
func imagesIn(t *testing.T, req fake.Request) [][]string {
	t.Helper()
	out := make([][]string, len(req.Messages))
	for i, raw := range req.Messages {
		var m struct{ Images []string }
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out[i] = m.Images
	}
	return out
}

func countImages(t *testing.T, req fake.Request) int {
	n := 0
	for _, images := range imagesIn(t, req) {
		n += len(images)
	}
	return n
}

func (r *rig) attachRecords() []log.Attach {
	var out []log.Attach
	for _, ev := range r.logged() {
		if ev.Kind == log.KindAttach {
			var a log.Attach
			ev.Decode(&a)
			out = append(out, a)
		}
	}
	return out
}

func TestImageAttachedAfterItsConfirmReachesTheModel(t *testing.T) {
	t.Parallel()
	r := imageRig(t, true, nil, fake.Turn{Content: "A grey gradient."})
	path, data := picture(t, "shot.png", 1200, 800)
	receipt, asked, err := r.attachAnswering(path, "shot.png", "y")
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(r.srv.URL, "http://")
	if asked == nil {
		t.Fatal("no confirm was asked")
	}
	reasons := strings.Join(asked.Prompt.Reasons, "\n")
	if !asked.Prompt.Security || asked.Prompt.Prim != "attach image" || asked.Prompt.Args != "shot.png" || asked.Options.Session || asked.Options.Turn {
		t.Fatalf("confirm: %+v", asked)
	}
	for _, want := range []string{"shot.png is an image (PNG, 1200×800", "about 1,105 tokens", "cannot be redacted", "fake-vision at " + host + ", on this machine", "the judge never gets it"} {
		if !strings.Contains(reasons, want) {
			t.Fatalf("the confirm does not say %q:\n%s", want, reasons)
		}
	}
	if receipt.Type != "image" || receipt.MediaType != "image/png" || receipt.Width != 1200 || receipt.Height != 800 || receipt.Tokens != 1105 ||
		receipt.Destination != host || receipt.ApprovedBy != "unix:answerer" || receipt.Actor != "unix:attacher" || receipt.Bytes != int64(len(data)) {
		t.Fatalf("receipt: %+v", receipt)
	}
	stored, err := os.ReadFile(filepath.Join(r.repo, receipt.Stored))
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("stored copy: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	if strings.Contains(r.logb.String(), encoded[:64]) || len(r.attachRecords()) != 1 {
		t.Fatal("the log holds the pixels, or no attach record")
	}

	if e := r.turn("what is this?"); e != "" {
		t.Fatal(e)
	}
	images := imagesIn(t, r.srv.Requests[0])
	if len(images[1]) != 1 || images[1][0] != encoded {
		t.Fatalf("the request does not carry the image: %v", len(images[1]))
	}
	if !strings.Contains(string(r.srv.Requests[0].Raw), `Attached image \"shot.png\" (PNG, 1200×800)`) {
		t.Fatalf("no line for the image:\n%.600s", r.srv.Requests[0].Raw)
	}
	// Asked once whether the model takes images, and the request replays
	// by the image's digest.
	if r.srv.Shows() != 1 || replayAll(t, r.logb) != 1 {
		t.Fatalf("shows %d", r.srv.Shows())
	}
	reqs, _ := r.requests()
	if len(reqs[0].Withheld) != 0 {
		t.Fatalf("withheld: %+v", reqs[0].Withheld)
	}
}

func TestImageNotAttachedWithoutAYes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, want string
		adjust     func(*Options)
		asks       bool
	}{
		{"declined", "its confirm was declined", nil, true},
		{"no-confirm", "answers every confirm with no", func(o *Options) { o.NoConfirm = true }, false},
		{"yolo", "never answers a security confirm", func(o *Options) { o.AutoApprove = true }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := imageRig(t, true, c.adjust)
			path, _ := picture(t, "secret.png", 64, 64)
			_, asked, err := r.attachAnswering(path, "secret.png", "n")
			if err == nil || !strings.Contains(err.Error(), c.want) || (asked != nil) != c.asks {
				t.Fatalf("err %v, asked %v", err, asked)
			}
			if len(r.attachRecords()) != 0 || !strings.Contains(strings.Join(r.notes(), "\n"), "image not attached: secret.png") {
				t.Fatalf("records %+v, notes %v", r.attachRecords(), r.notes())
			}
			if stored, _ := filepath.Glob(filepath.Join(r.repo, ".nodwyn", "attachments", "*", "*")); len(stored) != 0 {
				t.Fatalf("kept a copy: %v", stored)
			}
		})
	}
}

// While an image waits for its confirm no turn starts, and stopping the
// session's work cancels it.
func TestImageConfirmHoldsTheSession(t *testing.T) {
	t.Parallel()
	r := imageRig(t, true, nil)
	path, _ := picture(t, "wait.png", 8, 8)
	done := make(chan error, 1)
	go func() {
		_, err := r.s.Attach(path, "wait.png", "unix:attacher")
		done <- err
	}()
	for asked := false; !asked; {
		select {
		case ev := <-r.s.Events():
			asked = ev.Kind == event.PromptConfirm
		case <-time.After(10 * time.Second):
			t.Fatal("no confirm was asked")
		}
	}
	if err := r.s.Send("go on"); err == nil || !strings.Contains(err.Error(), "attachment is in progress") {
		t.Fatalf("a turn started during the confirm: %v", err)
	}
	if _, err := r.s.Attach(path, "wait.png", "unix:attacher"); err == nil {
		t.Fatal("a second attachment began")
	}
	r.s.Abort()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("aborted attach: %v", err)
	}
	if len(r.attachRecords()) != 0 {
		t.Fatal("attached after an abort")
	}
}

func TestImageRefusedWhenTheControlModelTakesNone(t *testing.T) {
	t.Parallel()
	r := imageRig(t, false, nil)
	path, _ := picture(t, "chart.png", 32, 32)
	_, asked, err := r.attachAnswering(path, "chart.png", "y")
	if err == nil || asked != nil || !strings.Contains(err.Error(), "fake-vision does not take images") || !strings.Contains(err.Error(), "vision model") {
		t.Fatalf("err %v, asked %v", err, asked)
	}
	// A chat-completions server does not say; its configuration does.
	oai := newFakeOpenAI(t, oaiText("A chart."))
	r = imageRig(t, false, func(o *Options) {
		o.Provider = nil
		o.Config.Control = config.Model{Provider: "openai", URL: oai.URL + "/v1", Model: "gpt-vision"}
	})
	if _, _, err := r.attachAnswering(path, "chart.png", "y"); err == nil || !strings.Contains(err.Error(), "add vision = true to [control]") {
		t.Fatalf("openai without vision: %v", err)
	}
	seeing := newFakeOpenAI(t, oaiText("models"), oaiText("A chart."))
	r = imageRig(t, false, func(o *Options) {
		o.Provider = nil
		o.Config.Control = config.Model{Provider: "openai", URL: seeing.URL + "/v1", Model: "gpt-vision", Vision: true}
	})
	if _, asked, err := r.attachAnswering(path, "chart.png", "y"); err != nil || asked == nil {
		t.Fatalf("openai with vision: %v", err)
	}
	if e := r.turn("read the chart"); e != "" {
		t.Fatal(e)
	}
	var sent struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	for _, req := range seeing.seen() {
		if strings.Contains(req.body, `"messages"`) {
			json.Unmarshal([]byte(req.body), &sent)
		}
	}
	if len(sent.Messages) < 2 || !strings.Contains(string(sent.Messages[1].Content), `{"type":"image_url","image_url":{"url":"data:image/png;base64,`) {
		t.Fatalf("chat-completions request: %+v", sent)
	}
}

func TestImageAttachmentChecksTheFile(t *testing.T) {
	t.Parallel()
	r := imageRig(t, true, nil)
	fakePNG := filepath.Join(t.TempDir(), "photo.png")
	os.WriteFile(fakePNG, []byte("GIF89a\x01\x00\x01\x00"), 0o600)
	if _, asked, err := r.attachAnswering(fakePNG, "photo.png", "y"); err == nil || asked != nil || !strings.Contains(err.Error(), "is not a PNG image: its content is GIF") {
		t.Fatalf("a .png that is a GIF: %v", err)
	}
	huge, _ := picture(t, "huge.png", 9000, 10)
	if _, asked, err := r.attachAnswering(huge, "huge.png", "y"); err == nil || asked != nil || !strings.Contains(err.Error(), "at most 8000 pixels") {
		t.Fatalf("a 9000-pixel image: %v", err)
	}
	if r.srv.Shows() != 0 {
		t.Fatal("asked the model before the file was checked")
	}
}

// A stored image that changes or disappears is never sent silently: the
// request says so instead, and still replays.
func TestChangedOrMissingImageIsSaidSo(t *testing.T) {
	t.Parallel()
	r := imageRig(t, true, nil, fake.Turn{Content: "one"}, fake.Turn{Content: "two"}, fake.Turn{Content: "three"})
	path, data := picture(t, "plan.png", 40, 30)
	receipt, _, err := r.attachAnswering(path, "plan.png", "y")
	if err != nil {
		t.Fatal(err)
	}
	stored := filepath.Join(r.repo, receipt.Stored)
	changed := append([]byte(nil), data...)
	changed[len(changed)-20] ^= 0xff
	os.WriteFile(stored, changed, 0o600)
	if e := r.turn("look"); e != "" {
		t.Fatal(e)
	}
	os.Remove(stored)
	if e := r.turn("look again"); e != "" {
		t.Fatal(e)
	}
	os.WriteFile(stored, data, 0o600)
	if e := r.turn("and now?"); e != "" {
		t.Fatal(e)
	}
	for i, want := range []string{"no longer matches the image that was attached", "its stored copy is missing"} {
		if countImages(t, r.srv.Requests[i]) != 0 || !strings.Contains(string(r.srv.Requests[i].Raw), want) {
			t.Fatalf("request %d:\n%.800s", i, r.srv.Requests[i].Raw)
		}
	}
	if countImages(t, r.srv.Requests[2]) != 1 {
		t.Fatal("the restored image is not sent")
	}
	reqs, _ := r.requests()
	if len(reqs[0].Withheld) != 1 || reqs[0].Withheld[0].Reason != "changed" || reqs[1].Withheld[0].Reason != "missing" || len(reqs[2].Withheld) != 0 {
		t.Fatalf("withheld: %+v %+v %+v", reqs[0].Withheld, reqs[1].Withheld, reqs[2].Withheld)
	}
	if n := replayAll(t, r.logb); n != 3 {
		t.Fatalf("replayed %d of 3", n)
	}
}

// The planner, a sub-agent and the judge never get an image; another host
// gets a line saying the image is not for it.
func TestImagesStayOutOfNarrowedContextsAndTheJudge(t *testing.T) {
	t.Parallel()
	judge := newFakeJudge(t, noul(0.9), score(3.6))
	other := fake.NewT(t, fake.Turn{Content: "I see no image."})
	t.Cleanup(other.Close)
	other.SetCapabilities("completion", "vision")
	r := imageRig(t, true, func(o *Options) {
		os.MkdirAll(filepath.Join(o.Repo, ".nodwyn"), 0o755)
		os.WriteFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"), []byte(`
(define plan-explore-rounds 0)
(define (agent-turn request)
  (plan-gate request)
  (let ((verdict (sub-agent "You review." "Review the request." '() "reviewer")))
    (add-message verdict)
    (model-turn)
    (model-turn "other")))`), 0o644)
		kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
		if err != nil {
			t.Fatal(err)
		}
		kc.Add("jev-key", []byte("sk-judge-canary-0001"))
		o.Keychain = kc
		o.Bindings = binding.Table{Bind: []binding.Binding{{Class: "jev-key", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}}}}
		o.Config.Jev.URL, o.Config.Jev.Content = judge.URL, config.JevContentPlan
		o.Config.Models = map[string]config.Model{"other": {URL: other.URL, Model: "other-vision"}}
	},
		fake.Turn{Content: "1. Describe the image."}, // planner
		fake.Turn{Content: "Looks fine."},            // reviewer
		fake.Turn{Content: "A gradient."},            // control
	)
	path, data := picture(t, "screen.png", 300, 200)
	receipt, _, err := r.attachAnswering(path, "screen.png", "y")
	if err != nil {
		t.Fatal(err)
	}
	if e := r.turn("describe the image"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 3 || len(r.gatePlans()) != 1 || r.gatePlans()[0].Plan != "1. Describe the image." {
		t.Fatalf("requests %d, plans %+v", len(r.srv.Requests), r.gatePlans())
	}
	planner, reviewer, control := r.srv.Requests[0], r.srv.Requests[1], r.srv.Requests[2]
	if !strings.Contains(string(planner.Raw), "planner") || countImages(t, planner) != 0 || strings.Contains(string(planner.Raw), "screen.png") {
		t.Fatalf("the planner's request:\n%.600s", planner.Raw)
	}
	if countImages(t, reviewer) != 0 || strings.Contains(string(reviewer.Raw), "screen.png") {
		t.Fatalf("the sub-agent's request:\n%.600s", reviewer.Raw)
	}
	if countImages(t, control) != 1 {
		t.Fatal("the control model did not get the image")
	}
	// Another configured model is another host: the image was not confirmed
	// for it.
	elsewhere := string(other.Requests[0].Raw)
	if countImages(t, other.Requests[0]) != 0 || !strings.Contains(elsewhere, "attached to go to "+receipt.Destination+" only") {
		t.Fatalf("another host's request:\n%.800s", elsewhere)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	judge.mu.Lock()
	bodies := append([]string(nil), judge.bodies...)
	judge.mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("the judge was asked %d times", len(bodies))
	}
	for _, body := range bodies {
		if strings.Contains(body, encoded[:64]) || strings.Contains(body, receipt.SHA256) || strings.Contains(body, "image/png") || strings.Contains(body, "screen.png") {
			t.Fatalf("an image reached the judge:\n%s", body)
		}
	}
	if n := replayAll(t, r.logb); n != 4 {
		t.Fatalf("replayed %d of 4", n)
	}
	u, _ := url.Parse(other.URL)
	reqs, _ := r.requests()
	if last := reqs[len(reqs)-1]; last.Endpoint != "other" || len(last.Withheld) != 1 || last.Withheld[0].Reason != "elsewhere" || u.Host == receipt.Destination {
		t.Fatalf("the other model's record: %+v", last)
	}
}
