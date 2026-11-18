package state

import (
	"bytes"
	"fmt"
	"testing"
)

// runStoreSuite checks the behaviour every Store implementation must share.
func runStoreSuite(t *testing.T, open func(t *testing.T) Store) {
	t.Run("ReadOwnWrites", func(t *testing.T) {
		s := open(t)
		tx := s.Begin()
		tx.Set([]byte("a/1"), []byte("x"))
		if v, ok, _ := tx.Get([]byte("a/1")); !ok || string(v) != "x" {
			t.Fatal("tx must see its own write")
		}
		tx.Delete([]byte("a/1"))
		if _, ok, _ := tx.Get([]byte("a/1")); ok {
			t.Fatal("tx must see its own delete")
		}
		tx.Discard()
	})
	t.Run("InvisibleUntilCommit", func(t *testing.T) {
		s := open(t)
		tx := s.Begin()
		tx.Set([]byte("k"), []byte("v"))
		snap := s.Snapshot()
		if _, ok, _ := snap.Get([]byte("k")); ok {
			t.Fatal("uncommitted write visible to snapshot")
		}
		snap.Close()
		if err := tx.Commit(true); err != nil {
			t.Fatal(err)
		}
		snap = s.Snapshot()
		defer snap.Close()
		if v, ok, _ := snap.Get([]byte("k")); !ok || string(v) != "v" {
			t.Fatal("committed write not visible")
		}
	})
	t.Run("SnapshotIsStable", func(t *testing.T) {
		s := open(t)
		commit(t, s, map[string]string{"k": "1"})
		snap := s.Snapshot()
		defer snap.Close()
		commit(t, s, map[string]string{"k": "2"})
		if v, _, _ := snap.Get([]byte("k")); string(v) != "1" {
			t.Fatalf("snapshot moved: %s", v)
		}
	})
	t.Run("DiscardDropsWrites", func(t *testing.T) {
		s := open(t)
		tx := s.Begin()
		tx.Set([]byte("k"), []byte("v"))
		tx.Discard()
		snap := s.Snapshot()
		defer snap.Close()
		if _, ok, _ := snap.Get([]byte("k")); ok {
			t.Fatal("discarded write applied")
		}
	})
	t.Run("ScanMergesWritesInOrder", func(t *testing.T) {
		s := open(t)
		commit(t, s, map[string]string{"p/1": "a", "p/3": "c", "p/5": "e", "q/1": "z", "o/9": "z"})
		tx := s.Begin()
		defer tx.Discard()
		tx.Set([]byte("p/2"), []byte("b"))
		tx.Delete([]byte("p/3"))
		tx.Set([]byte("p/5"), []byte("E"))
		want := []string{"p/1=a", "p/2=b", "p/5=E"}
		if got := keys(t, tx.Scan([]byte("p/"))); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("scan got %v want %v", got, want)
		}
		rev := []string{"p/5=E", "p/2=b", "p/1=a"}
		if got := keys(t, tx.ScanReverse([]byte("p/"))); fmt.Sprint(got) != fmt.Sprint(rev) {
			t.Fatalf("reverse scan got %v want %v", got, rev)
		}
	})
	t.Run("PrefixWithFFBytes", func(t *testing.T) {
		s := open(t)
		commit(t, s, map[string]string{"x\xff\xff": "1", "x\xff\xff\x01": "2", "y": "3"})
		snap := s.Snapshot()
		defer snap.Close()
		if got := keys(t, snap.Scan([]byte("x\xff\xff"))); len(got) != 2 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("Wipe", func(t *testing.T) {
		s := open(t)
		commit(t, s, map[string]string{"a": "1", "m/applied_lsn": "2"})
		if err := s.Wipe(); err != nil {
			t.Fatal(err)
		}
		snap := s.Snapshot()
		defer snap.Close()
		if got := keys(t, snap.Scan(nil)); len(got) != 0 {
			t.Fatalf("wipe left %v", got)
		}
	})
	t.Run("AddWeightRemovesZero", func(t *testing.T) {
		s := open(t)
		tx := s.Begin()
		k := []byte("w/1")
		if _, now, _ := AddWeight(tx, k, 2); now != 2 {
			t.Fatal(now)
		}
		if old, now, _ := AddWeight(tx, k, -2); old != 2 || now != 0 {
			t.Fatal(old, now)
		}
		if _, ok, _ := tx.Get(k); ok {
			t.Fatal("zero weight stored")
		}
		tx.Discard()
	})
}

func commit(t *testing.T, s Store, kv map[string]string) {
	t.Helper()
	tx := s.Begin()
	for k, v := range kv {
		tx.Set([]byte(k), []byte(v))
	}
	if err := tx.Commit(true); err != nil {
		t.Fatal(err)
	}
}

func keys(t *testing.T, it Iterator) []string {
	t.Helper()
	kvs, err := Collect(it)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, kv := range kvs {
		out = append(out, string(kv.Key)+"="+string(kv.Value))
	}
	return out
}

func TestMemoryStore(t *testing.T) {
	runStoreSuite(t, func(*testing.T) Store { return NewMemory() })
}

func TestPrefixEnd(t *testing.T) {
	cases := map[string]string{"a": "b", "a\xff": "b", "ab": "ac", "": ""}
	for in, want := range cases {
		got := PrefixEnd([]byte(in))
		if !bytes.Equal(got, []byte(want)) && !(want == "" && got == nil) {
			t.Errorf("PrefixEnd(%q) = %q want %q", in, got, want)
		}
	}
}
