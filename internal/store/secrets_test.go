package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSecretSlotRoundTripsAndReplaces(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, ok, err := st.GetSecret(ctx, "a", "t"); ok || err != nil {
		t.Fatalf("empty slot: ok=%v err=%v", ok, err)
	}
	exp := time.Date(2027, 1, 2, 3, 4, 5, 6, time.UTC)
	if err = st.PutSecret(ctx, "a", "t", []byte{1, 2, 3}, exp); err != nil {
		t.Fatal(err)
	}
	if err = st.PutSecret(ctx, "a", "t", []byte{9}, exp.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	sealed, got, ok, err := st.GetSecret(ctx, "a", "t")
	if err != nil || !ok || len(sealed) != 1 || sealed[0] != 9 || !got.Equal(exp.Add(time.Hour)) {
		t.Fatalf("sealed=%v exp=%v ok=%v err=%v", sealed, got, ok, err)
	}
	if _, _, ok, _ = st.GetSecret(ctx, "b", "t"); ok {
		t.Fatal("slot leaked across agents")
	}
}
