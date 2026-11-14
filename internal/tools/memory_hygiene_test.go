package tools_test

import (
	"context"
	"strings"
	"testing"
)

// A ref such as r58 restarts at r1 every wake, so no durable write may name one.
func TestWakeRefsAreRefusedInDurableText(t *testing.T) {
	st, _, d := setup(t)
	for _, tc := range []struct{ tool, args string }{
		{"note_write", `{"topic":"robots","text":"r58 got 63 likes, reply there again"}`},
		{"note_write", `{"topic":"reply r58","text":"fine"}`},
		{"note_write", `{"topic":"robots","description":"see r3","text":"fine"}`},
		{"strategy_write", `{"text":"keep answering r58","citations":["action:1"]}`},
	} {
		_, err := d.Dispatch(context.Background(), call(tc.tool, tc.args))
		if err == nil || !strings.Contains(err.Error(), "stale_ref") {
			t.Errorf("%s %s: %v", tc.tool, tc.args, err)
		}
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d notes written", n)
	}
	var result string
	if err := st.DB().QueryRow(`SELECT result_json FROM tool_calls WHERE tool='note_write' ORDER BY id LIMIT 1`).Scan(&result); err != nil || !strings.Contains(result, `"reason":"stale_ref"`) {
		t.Fatalf("audit row %q: %v", result, err)
	}
}

func TestOrdinaryVietnameseTextIsNotAStaleRef(t *testing.T) {
	_, _, d := setup(t)
	for i, text := range []string{
		"mn ơi hôm nay r thấy MV mới ổn áp vl",
		"robot R2D2 và tầng 5 nhà xe, r2d2 vẫn hot",
		"bài về RTX 4090 giá 59 triệu, mình mê",
		"thread hô tim đi mn, 2026 năm của r",
	} {
		args := `{"topic":"topic ` + string(rune('a'+i)) + `","text":"` + text + `"}`
		if _, err := d.Dispatch(context.Background(), call("note_write", args)); err != nil {
			t.Errorf("%q refused: %v", text, err)
		}
	}
}

func TestDatedNoteTopicIsRefused(t *testing.T) {
	st, _, d := setup(t)
	_, err := d.Dispatch(context.Background(), call("note_write", `{"topic":"2026-11-14 stale requested replies","text":"diary"}`))
	if err == nil || !strings.Contains(err.Error(), "dated_topic") {
		t.Fatalf("dated topic: %v", err)
	}
	if _, err = d.Dispatch(context.Background(), call("note_write", `{"topic":"Giờ đăng bài 2026","text":"ok"}`)); err != nil {
		t.Fatalf("undated topic refused: %v", err)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d notes", n)
	}
}

func TestDreamMemoryAddRefusesAWakeRef(t *testing.T) {
	st, d, _ := dreamSetup(t)
	_, err := d.Dispatch(context.Background(), call("memory_add", `{"text":"r58 works","action_id":1}`))
	if err == nil || !strings.Contains(err.Error(), "stale_ref") {
		t.Fatalf("err %v", err)
	}
	if index, _ := st.MemoryIndex(context.Background(), "agent", true); len(index) != 0 {
		t.Fatalf("index %v", index)
	}
	if _, err = d.Dispatch(context.Background(), call("memory_add", `{"text":"Short roasts work","action_id":1}`)); err != nil {
		t.Fatalf("normal lesson refused: %v", err)
	}
}
