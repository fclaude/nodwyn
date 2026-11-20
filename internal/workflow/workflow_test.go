package workflow

import (
	"os"
	"testing"
)

func TestAppendAfterInterruptedRecord(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	id := NewID()
	if _, err := s.Append(id, KindDefine, Define{Definition: Definition{Name: "Original"}}); err != nil {
		t.Fatal(err)
	}
	p, err := s.path(id)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("{\"seq\":2,"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := s.Append(id, KindDefine, Define{Definition: Definition{Name: "Updated"}})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Definition.Name != st.Definition.Name {
		t.Fatalf("save returned %q; reload returned %q", st.Definition.Name, replay.Definition.Name)
	}
}

func TestAppendRetainsCompleteRecordMissingItsNewline(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	id := NewID()
	if _, err := store.Append(id, KindDefine, Define{Definition: Definition{Name: "Original"}}); err != nil {
		t.Fatal(err)
	}
	path, err := store.path(id)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(id, KindSeen, Seen{Items: map[string]string{"a": "v1"}}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Definition == nil || state.Definition.Name != "Original" || state.Seen["a"] != "v1" {
		t.Fatalf("incomplete replay: %+v", state)
	}
}
