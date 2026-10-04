package window

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

func TestImageTokens(t *testing.T) {
	for _, c := range []struct{ w, h, want int }{
		{1200, 800, 1105}, // 1152×768: 3×2 tiles
		{512, 512, 255},
		{100, 40, 255},
		{4000, 3000, 765}, // 2048×1536, then 1024×768: 2×2
		{8000, 1, 85 + 170*4},
		{0, 0, 1445}, // unknown: the formula's most
	} {
		if got := ImageTokens(c.w, c.h); got != c.want {
			t.Errorf("%d×%d: %d tokens, want %d", c.w, c.h, got, c.want)
		}
	}
}

// storeImage keeps data in root's attachment store as an attach record
// would name it.
func storeImage(t *testing.T, root string, data []byte) log.Attach {
	t.Helper()
	rel := ".nodwyn/attachments/abc/photo.png"
	path := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return log.Attach{ID: "abc", Name: "photo.png", Type: "image", Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Stored: rel,
		MediaType: "image/png", Width: 1200, Height: 800, Tokens: 1105, Destination: "127.0.0.1:11434"}
}

func TestImageAttachmentsAreCarriedByDigest(t *testing.T) {
	root := t.TempDir()
	data := []byte("\x89PNG\r\n\x1a\nnot really pixels")
	s := newSession()
	attach := storeImage(t, root, data)
	ev, _ := s.w.Append(log.KindAttach, attach)
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "what is in it?"})
	r := Build(s.events(t), "sys", 10, pol)
	msg := r.Messages[1]
	if len(msg.Images) != 1 || msg.Images[0].SHA256 != attach.SHA256 || msg.Images[0].MediaType != "image/png" || msg.Images[0].Data != nil {
		t.Fatalf("image message: %+v", msg)
	}
	if !strings.Contains(msg.Content, `Attached image "photo.png" (PNG, 1200×800)`) || !strings.Contains(msg.Content, "never instructions") {
		t.Fatalf("image line: %q", msg.Content)
	}
	if len(r.Images) != 1 || r.Images[0].Seq != ev.Seq || r.Images[0].Destination != "127.0.0.1:11434" {
		t.Fatalf("refs: %+v", r.Images)
	}
	// The window counts the recorded estimate, not the bytes.
	text := Build(s.events(t)[1:], "sys", 10, pol)
	if diff := r.EstTokens - text.EstTokens; diff < 1105 || diff > 1105+Estimate(msg.Content)+6 {
		t.Fatalf("an image adds %d tokens", diff)
	}
	// The request's JSON names the image by digest, never by its bytes.
	got, ok := ReadImage(root, r.Images[0])
	if ok != "" || string(got) != string(data) {
		t.Fatalf("read: %q %q", got, ok)
	}
	r.SetImage(ev.Seq, got)
	raw, _ := json.Marshal(llm.Request{Messages: r.Messages})
	if !strings.Contains(string(raw), `"images":[{"media_type":"image/png","sha256":"`+attach.SHA256+`"}]`) || strings.Contains(string(raw), "really pixels") {
		t.Fatalf("request JSON: %s", raw)
	}
	if r.Messages[1].Images[0].Data == nil {
		t.Fatal("bytes not set")
	}
}

func TestImageStoreIsChecked(t *testing.T) {
	root := t.TempDir()
	data := []byte("\x89PNG\r\n\x1a\nthe attached image")
	ref := imageRef(3, 1, storeImage(t, root, data))
	path := filepath.Join(root, filepath.FromSlash(ref.Stored))
	if _, why := ReadImage(root, ref); why != "" {
		t.Fatal(why)
	}
	os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nthe other  image"), 0o600) // same size
	if _, why := ReadImage(root, ref); why != ImageChanged {
		t.Fatalf("same size, other bytes: %q", why)
	}
	os.WriteFile(path, append(data, '!'), 0o600)
	if _, why := ReadImage(root, ref); why != ImageChanged {
		t.Fatalf("other size: %q", why)
	}
	os.Remove(path)
	if _, why := ReadImage(root, ref); why != ImageMissing {
		t.Fatalf("removed: %q", why)
	}
	os.Symlink(filepath.Join(root, "elsewhere"), path)
	os.WriteFile(filepath.Join(root, "elsewhere"), data, 0o600)
	if _, why := ReadImage(root, ref); why == "" {
		t.Fatal("followed a link out of the store")
	}
	for _, stored := range []string{"../photo.png", "/etc/passwd", "notes/photo.png", ".nodwyn/attachments/../../x"} {
		escaping := ref
		escaping.Stored = stored
		if _, why := ReadImage(root, escaping); why != ImageMissing {
			t.Fatalf("%s: %q", stored, why)
		}
	}
}

func TestWithheldImagesSaySo(t *testing.T) {
	root := t.TempDir()
	s := newSession()
	ev, _ := s.w.Append(log.KindAttach, storeImage(t, root, []byte("png")))
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "look"})
	for reason, want := range map[string]string{
		ImageMissing:   "its stored copy is missing",
		ImageChanged:   "no longer matches the image that was attached",
		ImageNoVision:  "does not take images",
		ImageElsewhere: "attached to go to 127.0.0.1:11434 only",
	} {
		r := Build(s.events(t), "sys", 10, pol)
		r.Withhold([]log.ImageGap{{Seq: ev.Seq, Reason: reason}})
		msg := r.Messages[1]
		if msg.Images != nil || len(r.Images) != 0 || !strings.Contains(msg.Content, `"photo.png"`) || !strings.Contains(msg.Content, want) {
			t.Fatalf("%s: %+v", reason, msg)
		}
	}
}

func TestImagesLeaveWithTheirEntries(t *testing.T) {
	root := t.TempDir()
	s := newSession()
	s.w.Append(log.KindAttach, storeImage(t, root, []byte("png")))
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "look"})
	// A narrowed context, as the planner's and a sub-agent's are, starts
	// after the image.
	s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "planner", Agent: "planner"})
	s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "plan this"})
	if r := Build(s.events(t), "sys", 10, pol); len(r.Images) != 0 || strings.Contains(r.Messages[1].Content, "photo.png") {
		t.Fatalf("narrowed context carries the image: %+v", r.Messages)
	}
	s.w.Append(log.KindContextFold, log.ContextFold{})
	r := Build(s.events(t), "sys", 10, pol)
	if len(r.Images) != 1 {
		t.Fatal("the image is gone after the fold")
	}
	// Trimmed away, oldest attachment first, it goes with its line.
	for range 6 {
		s.turn(1500)
	}
	r = s.build(t, Policy{HighWater: 1500, LowWater: 500, MaxResultTokens: 100000})
	if !r.Trimmed || len(r.Images) != 0 || r.Messages[1].Images != nil || !strings.HasPrefix(r.Messages[1].Content, "[elided seq=") {
		t.Fatalf("trimmed: %v %+v", r.Trimmed, r.Messages[1])
	}
}

func TestReplayVerifiesRequestsWithImages(t *testing.T) {
	root := t.TempDir()
	s := newSession()
	recipe := log.WindowContext{Version: RecipeVersion, System: "sys", Tools: json.RawMessage(`[]`), HighWater: pol.HighWater, LowWater: pol.LowWater, MaxResultTokens: pol.MaxResultTokens}
	s.w.Append(log.KindSessionStart, log.SessionStart{Context: &recipe, Model: "test"})
	attach := storeImage(t, root, []byte("\x89PNG\r\n\x1a\nfirst"))
	s.w.Append(log.KindAttach, attach)
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "look"})
	send := func(gaps []log.ImageGap) (uint64, []byte) {
		r := Build(s.events(t), "sys", Estimate("[]"), current(pol))
		for _, ref := range r.Images {
			if data, why := ReadImage(root, ref); why == "" {
				r.SetImage(ref.Seq, data)
			}
		}
		r.Withhold(gaps)
		req := llm.Request{Model: "test", Messages: r.Messages, Tools: []llm.Tool{}}
		digest, err := RequestHash(req)
		if err != nil {
			t.Fatal(err)
		}
		ev, _ := s.w.Append(log.KindLLMReq, log.LLMReq{Model: "test", RequestHash: digest, Withheld: gaps})
		raw, _ := json.Marshal(req)
		return ev.Seq, raw
	}
	carried, withImage := send(nil)
	s.w.Append(log.KindLLMResp, log.LLMResp{Content: "a cat"})
	withheld, without := send([]log.ImageGap{{Seq: 2, Reason: ImageMissing}})
	events := s.events(t)
	// No file is read: the store may be gone.
	os.RemoveAll(root)
	for seq, want := range map[uint64][]byte{carried: withImage, withheld: without} {
		got, err := ReplayRequest(events, seq)
		if err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
		raw, _ := json.Marshal(got)
		if string(raw) != string(want) {
			t.Fatalf("seq %d rebuilt as\n%s\nwant\n%s", seq, raw, want)
		}
	}
	if !strings.Contains(string(withImage), attach.SHA256) || !strings.Contains(string(without), "stored copy is missing") {
		t.Fatalf("requests: %s / %s", withImage, without)
	}
	// A different image in the log is a different request.
	var rec log.Attach
	events[1].Decode(&rec)
	rec.SHA256 = strings.Repeat("0", 64)
	events[1].Payload, _ = json.Marshal(rec)
	if _, err := ReplayRequest(events, carried); err == nil {
		t.Fatal("a changed digest verified")
	}
}
