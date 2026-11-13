package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// The fixed set behind the tokenizer decision in docs/learning.md. Documents are the
// agent's own voice on the topics of templates/*.agent.yaml;
// every group pairs a concept with look-alikes that differ only by Vietnamese tone marks.
var tokenizerDocs = []struct {
	text  string
	group string
}{
	{"vibe code xong mã nguồn rối như tơ vò, dọn mất cả đêm", "code"},
	{"dọn mã nguồn sau demo, nợ kỹ thuật chất cao", "code"},
	{"mã nguồn của con agent này chạy được là nhờ phước", "code"},
	{"con ma on-call hiện hồn lúc 2h sáng khi server sập", "oncall"},
	{"má tôi hỏi sao thức khuya, tôi bảo đang ship side project", "other"},
	{"benchmark này chạy mà không ai kiểm lại, số đẹp quá", "other"},
	{"giá token tăng gấp đôi, hoá đơn cloud đau ví", "price"},
	{"thấy ai wrapper giá $29 một tháng chỉ để tóm tắt một câu sql", "price"},
	{"gia đình tôi không hiểu agent là gì", "other"},
	{"dev già vẫn on-call lúc 2h sáng", "oncall"},
	{"câu trêu bị bơ hoàn toàn, 0 view", "ignored"},
	{"bài hôm qua bị bơ, chắc toàn bot nhìn nhau", "ignored"},
	{"bố tôi hỏi agent là gì, tôi nói là con bot", "other"},
	{"bờ vai dev gánh cả server năm đô", "other"},
	{"chuyển sang model mới vì giá rẻ hơn", "other"},
	{"server sập lúc 2h sáng, ngồi nhìn màn hình khóc", "oncall"},
	{"nợ kỹ thuật sau mùa vibe code còn nguyên", "debt"},
	{"nó chạy được trên máy tôi, nó sập trên prod", "other"},
	{"nở rộ tool mới mỗi ngày, nó nào cũng đòi trả phí", "other"},
	{"nợ kỹ thuật cộng dồn, trả không nổi", "debt"},
	{"dự án side project không ai xài, nhưng tôi vẫn ship", "project"},
	{"dự án indie sống bằng cà phê và hy vọng", "project"},
	{"dữ liệu benchmark bị chọn lọc để khoe số", "other"},
	{"tin tức model mới nhất trên hacker news hôm nay", "news"},
	{"tín hiệu thị trường ảo thật sự, ai cũng nói về agent", "other"},
	{"tin nóng product hunt lại thêm một con ai wrapper", "news"},
	{"đỉnh nóc kịch trần, cái demo chạy đúng một lần", "demo"},
	{"dính bug race condition giữa đêm", "other"},
}

var tokenizerQueries = []struct{ query, group string }{
	{"mã nguồn", "code"},
	{"giá token", "price"},
	{"bị bơ", "ignored"},
	{"2h sáng", "oncall"},
	{"nợ kỹ thuật", "debt"},
	{"dự án", "project"},
	{"tin tức", "news"},
	{"đỉnh nóc", "demo"},
}

func stripMarks(s string) string {
	r := strings.NewReplacer("ã", "a", "ồ", "o", "ợ", "o", "ỹ", "y", "ậ", "a", "ự", "u", "ứ", "u", "ơ", "o", "á", "a", "à", "a", "ạ", "a", "ó", "o", "ố", "o", "ổ", "o", "ú", "u", "ì", "i", "í", "i", "ế", "e", "ệ", "e", "ê", "e", "â", "a", "ô", "o", "ư", "u", "ở", "o", "ữ", "u", "ả", "a", "ầ", "a", "ũ", "u")
	return r.Replace(s)
}

// scoreTokenizer returns mean precision and recall of the code-built OR query
// over the fixed set under one unicode61 setting.
func scoreTokenizer(t *testing.T, removeDiacritics int, strip bool) (precision, recall float64) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "tok.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE d USING fts5(text, tokenize="unicode61 remove_diacritics %d")`, removeDiacritics)); err != nil {
		t.Fatal(err)
	}
	for i, doc := range tokenizerDocs {
		if _, err := db.Exec(`INSERT INTO d(rowid, text) VALUES (?, ?)`, i+1, doc.text); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range tokenizerQueries {
		text := q.query
		if strip {
			text = stripMarks(text)
		}
		rows, err := db.Query(`SELECT rowid FROM d WHERE d MATCH ? ORDER BY bm25(d), rowid DESC LIMIT 10`, ftsMatch(text))
		if err != nil {
			t.Fatal(err)
		}
		hit, got := 0, 0
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			got++
			if tokenizerDocs[id-1].group == q.group {
				hit++
			}
		}
		rows.Close()
		want := 0
		for _, doc := range tokenizerDocs {
			if doc.group == q.group {
				want++
			}
		}
		if got > 0 {
			precision += float64(hit) / float64(got)
		}
		recall += float64(hit) / float64(want)
	}
	n := float64(len(tokenizerQueries))
	return precision / n, recall / n
}

func TestTokenizerMeasurement(t *testing.T) {
	for _, strip := range []bool{false, true} {
		for _, rd := range []int{0, 2} {
			p, r := scoreTokenizer(t, rd, strip)
			t.Logf("remove_diacritics=%d stripped_query=%v precision=%.3f recall=%.3f", rd, strip, p, r)
		}
	}
	p0, r0 := scoreTokenizer(t, 0, false)
	p2, r2 := scoreTokenizer(t, 2, false)
	if p0 <= p2 || r0 < r2 {
		t.Fatalf("accented queries: remove_diacritics 0 (p=%.3f r=%.3f) must beat 2 (p=%.3f r=%.3f)", p0, r0, p2, r2)
	}
	_, rStripped0 := scoreTokenizer(t, 0, true)
	_, rStripped2 := scoreTokenizer(t, 2, true)
	if rStripped0 >= rStripped2 {
		t.Fatalf("unaccented queries: the cost of 0 disappeared (recall %.3f vs %.3f); revisit the decision", rStripped0, rStripped2)
	}
}
