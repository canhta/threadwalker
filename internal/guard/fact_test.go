package guard

import "testing"

func TestFactProblemKeepsTheBriefsDetail(t *testing.T) {
	for _, c := range []struct {
		fact string
		text string
		bad  bool
	}{
		{"bài nói giá 150k một ly", "150k một ly mà vẫn xếp hàng", false},
		{"bài nói giá 150k một ly", "ly nào cũng đắt thế này sao", true},
		{"đoạn “ngoảnh lại vẫn thấy chị” trúng tim", "cái cảm giác có người ở đó để mình yên tâm", true},
		{"đoạn “ngoảnh lại vẫn thấy chị” trúng tim", "ngoảnh lại vẫn thấy chị là xỉu", false},
		{"Ra mắt MV mới của ATEEZ", "ateez comeback là hết ngủ", false},
		{"Ra mắt MV mới của ATEEZ", "comeback nào cũng hết ngủ", true},
		{"fan khen giọng ca", "bất kỳ câu nào", false}, // no anchor: not checked
		{"", "bất kỳ câu nào", false},
	} {
		if got := factProblem(c.fact, []string{c.text}) != ""; got != c.bad {
			t.Errorf("fact %q text %q: problem=%v want %v", c.fact, c.text, got, c.bad)
		}
	}
}
