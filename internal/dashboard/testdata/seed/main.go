// Command seed builds a fresh database holding fourteen synthetic days of one
// agent through the store's own API, then serves the dashboard on it. The
// README screenshots are captured from this page. Every name is invented.
//
//	go run ./internal/dashboard/testdata/seed -db /tmp/demo.db -listen 127.0.0.1:8099
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"sort"
	"time"

	"github.com/canhta/threadz/internal/dashboard"
	"github.com/canhta/threadz/internal/store"
)

const (
	agent    = "demo"
	username = "gsllm_demo"
	days     = 14
)

var posts = []string{
	"3 prompt mà mình dùng mỗi sáng để tóm tắt 50 email trong 5 phút. Cái thứ hai là cái mình hối hận vì không biết sớm hơn.",
	"Người ta bảo AI sẽ thay lập trình viên. Mình thấy nó thay đúng phần mình ghét nhất: viết regex và đọc log lúc 2 giờ sáng.",
	"Hôm nay thử cho agent tự đọc số liệu của chính nó. Kết quả: nó phát hiện bài đăng lúc 12h được xem gấp 3 lần bài lúc 15h.",
	"Một con số đáng nhớ: 80% câu trả lời hay trên Threads đến từ người đã đọc kỹ bài gốc. Đọc kỹ trước, trả lời sau.",
	"Mình từng nghĩ viết ngắn là lười. Hóa ra ngắn mới là phần khó: bỏ được 40% chữ mà ý vẫn nguyên.",
	"Unpopular opinion: đa số chatbot chăm sóc khách hàng tệ không phải vì model dở, mà vì không ai viết lại câu hỏi thường gặp.",
	"Giá xăng giảm lần thứ hai liên tiếp. Tính nhanh: đi làm 15km mỗi ngày, một tháng tiết kiệm được gần một bữa cà phê sáng.",
	"Có người hỏi mình: học lập trình năm nay có còn đáng không? Mình trả lời bằng một câu hỏi: bạn muốn viết code hay muốn giải quyết vấn đề?",
	"Tuần này mình đọc 12 bài về agent. 11 bài nói về công cụ, 1 bài nói về việc khi nào nên để agent dừng lại. Bài cuối đáng đọc nhất.",
	"Nếu chỉ được giữ một thói quen khi làm sản phẩm: mỗi tuần nói chuyện với một người dùng thật. Không cần khảo sát.",
	"Số liệu thật: bài có câu hỏi ở dòng đầu nhận nhiều bình luận hơn bài có câu khẳng định, cỡ 2 lần, trên 20 bài mình thử.",
	"Mình thử đổi giờ đăng từ 20h sang 12h. Lượt xem tăng, nhưng bình luận giảm. Ai ngồi xem điện thoại giờ nghỉ trưa chỉ lướt thôi.",
	"Câu hỏi cho các bạn làm data: dashboard nào trong công ty bạn thực sự có người mở ra mỗi sáng?",
	"Một lỗi mình hay gặp: đăng bài xong rồi trả lời bình luận sau 6 tiếng. Người đọc đã đi rồi. 30 phút đầu mới là lúc cuộc trò chuyện hình thành.",
	"Đọc changelog của một thư viện nhỏ và thấy dòng: 'xóa 3.000 dòng, không đổi hành vi'. Đó là kiểu bản cập nhật mình thích nhất.",
	"Hôm nay agent tự bỏ một giả thuyết sau 5 lần thử: bài có emoji không hơn bài không emoji. Gỡ bỏ cũng là một kết quả.",
	"Ba thứ khiến một bài kỹ thuật dễ đọc: một ví dụ chạy được, một con số cụ thể, và một câu nói thẳng điều gì không nên làm.",
	"Mình đo thử: trả lời một bài đang có 30 bình luận gần như không ai thấy. Trả lời bài có 3 bình luận thì được đọc.",
	"Cuối tuần thử tự host một công cụ nhỏ bằng SQLite. Một file, không dịch vụ nào, chạy ổn trên máy 5 đô một tháng.",
	"Hai tuần chạy thử, mình rút ra: thứ kéo người theo dõi không phải bài viral, mà là ba bài ổn định liên tiếp.",
}

var replyA = []string{
	"Đúng ý mình luôn,", "Chỗ này mình hơi khác một chút:", "Mình thử rồi và thấy", "Cảm ơn bạn đã chia sẻ,", "Góc nhìn hay đó,",
	"Có số liệu nào cho phần này không?", "Mình cũng gặp đúng tình huống này,", "Bổ sung thêm một ý nhỏ:", "Nghe hợp lý,", "Câu hỏi hay,",
}

var replyB = []string{
	"nhất là khi đội nhỏ và chưa có quy trình rõ ràng.", "nhưng nó phụ thuộc nhiều vào cách đo ban đầu.",
	"và kết quả sau hai tuần khá khác so với tuần đầu.", "mình sẽ thử áp dụng vào dự án tuần sau.",
}

var users = []string{"mai.codes", "hoang_ml", "linh.dev", "bao_data", "thu.writes", "nam_sre", "an.builds", "quynh_pm"}

var trendPool = []struct{ source, title string }{
	{"hn", "I gave an agent a lakehouse and control of the budget"}, {"hn", "Show HN: a tiny SQLite-backed job queue"},
	{"hn", "The hidden cost of model context"}, {"gt", "giá xăng dầu hôm nay"}, {"gt", "học lập trình năm 2026"},
	{"ph", "A small tool for reading logs"}, {"ph", "Neura Notes"}, {"gt", "lương lập trình viên"},
}

type event struct {
	at     time.Time
	kind   string
	idx    int
	wake   int64
	action int64
}

type result struct {
	views, likes, replies, reposts int64
}

func main() {
	dbPath := flag.String("db", "demo.db", "new database file (must not exist)")
	listen := flag.String("listen", "127.0.0.1:8099", "loopback address to serve on; empty seeds and exits")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if _, err := os.Stat(*dbPath); err == nil {
		log.Fatalf("%s exists; seed needs a fresh database", *dbPath)
	}
	s, err := store.Open(ctx, *dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	if err = seed(ctx, s, now); err != nil {
		log.Fatal(err)
	}
	if *listen == "" {
		return
	}
	h, err := dashboard.New(dashboard.Options{Source: s, Now: time.Now, Agents: []dashboard.Agent{{ID: agent, Username: username, DailyBudgetUSD: 0.5, Timezone: "Asia/Ho_Chi_Minh"}}})
	if err != nil {
		log.Fatal(err)
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("serving http://" + l.Addr().String())
	if err = dashboard.Serve(ctx, l, h); err != nil {
		log.Fatal(err)
	}
}

func seed(ctx context.Context, s *store.Store, now time.Time) error {
	rng := rand.New(rand.NewSource(7))
	must := func(err error) {
		if err != nil {
			log.Fatal(err)
		}
	}
	first := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	span := now.Sub(first)
	followersAt := func(t time.Time) int64 {
		x := math.Min(1, math.Max(0, float64(t.Sub(first))/float64(span)))
		return int64(80 + 320*math.Pow(x, 1.25))
	}
	must(s.EnsureAgent(ctx, agent))
	var err error

	// Wake slots (UTC+7 local): four runs a day at 08/12/16/20 and a plan at 22.
	type slot struct {
		at   time.Time
		kind string
	}
	var slots []slot
	for d := 0; d < days; d++ {
		day := first.AddDate(0, 0, d)
		for _, h := range []int{1, 5, 9, 13} {
			slots = append(slots, slot{day.Add(time.Duration(h) * time.Hour), "agent"})
		}
		slots = append(slots, slot{day.Add(15 * time.Hour), "dream"})
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].at.Before(slots[j].at) })
	var live []slot
	for _, sl := range slots {
		if !sl.at.Before(first.Add(30*time.Minute)) && sl.at.Before(now.Add(-45*time.Minute)) {
			live = append(live, sl)
		}
	}
	var agentWakes []int
	for i, sl := range live {
		if sl.kind == "agent" {
			agentWakes = append(agentWakes, i)
		}
	}
	// Which agent wakes publish: 20 posts and 40 replies spread evenly.
	postAt, replyAt := map[int]bool{}, map[int]bool{}
	n := len(agentWakes)
	for i := 0; i < 20; i++ {
		postAt[agentWakes[i*n/20]] = true
	}
	for i := 0; i < 40; i++ {
		replyAt[agentWakes[(i*n/40+1)%n]] = true
	}

	viralPost, flopPost := map[int]bool{4: true, 9: true, 15: true}, map[int]bool{2: true, 7: true, 12: true}
	viralReply, flopReply := map[int]bool{11: true}, map[int]bool{5: true, 20: true, 31: true}
	blocked := []string{"engagement_bait", "replies_per_user_7d", "blocked_phrase"}
	failSearchAt := len(live) / 2

	var events []event
	results := map[int64]result{}
	var postCount, replyCount, blockedCount, trendDay int
	lastTrend := -1
	for i, sl := range live {
		var wake int64
		if sl.kind == "agent" {
			wake, err = s.BeginWake(ctx, agent, sl.at, sl.at.Add(4*time.Hour))
		} else {
			key := sl.at.Format("2006-01-02T15")
			wake, err = s.BeginPhaseWake(ctx, agent, sl.kind, key, sl.at)
		}
		must(err)
		t := sl.at
		step := func(tool, outcome, reason, blockedBy string) {
			t = t.Add(time.Minute + time.Duration(rng.Intn(60))*time.Second)
			id, err := s.RecordToolAttempt(ctx, agent, wake, tool, "{}", blockedBy, t)
			must(err)
			if blockedBy == "" {
				body := `{"outcome":"` + outcome + `"}`
				if reason != "" {
					body = `{"outcome":"` + outcome + `","reason":"` + reason + `"}`
				}
				must(s.RecordToolResult(ctx, id, body))
			}
		}
		steps := 0
		do := func(tool string) { step(tool, "success", "", ""); steps++ }
		var base float64
		switch sl.kind {
		case "agent":
			do("read_own_metrics")
			do("find_posts")
			if i == failSearchAt {
				step("find_posts", "error", "timeout", "")
				steps++
			} else {
				do("find_posts")
			}
			if lastTrend != sl.at.YearDay() {
				do("fetch_trends")
				lastTrend = sl.at.YearDay()
				pick := rng.Perm(len(trendPool))[:6]
				var stories []store.TrendStory
				for r, p := range pick {
					stories = append(stories, store.TrendStory{Source: trendPool[p].source, Title: trendPool[p].title, URL: fmt.Sprintf("https://example.com/trend/%d", p), Score: 400 - 40*r})
				}
				must(s.RecordTrendStories(ctx, agent, stories, t, 10))
				trendDay++
			}
			must(s.RecordFollowers(ctx, agent, followersAt(t), t))
			if postAt[i] {
				idx := postCount
				postCount++
				id := publish(ctx, s, must, store.NewIntent{AgentID: agent, Kind: "post", Text: posts[idx], Hook: store.HookTypes[idx%len(store.HookTypes)], Rationale: "Chủ đề đang được quan tâm; câu mở đầu gợi tò mò.", At: t.Add(2 * time.Minute)}, fmt.Sprintf("https://www.threads.com/@%s/post/DR%05d", username, 100+idx*7), idx)
				do("post")
				base = 700
				switch {
				case viralPost[idx]:
					base = 9000
				case flopPost[idx]:
					base = 60
				}
				results[id] = pickResult(rng, base, 1, viralPost[idx], flopPost[idx])
				events = append(events, event{at: t.Add(2 * time.Minute), kind: "post", action: id})
			}
			if replyAt[i] {
				idx := replyCount
				replyCount++
				user := users[idx%len(users)]
				text := replyA[idx%len(replyA)] + " " + replyB[(idx/len(replyA)+idx)%len(replyB)]
				id := publish(ctx, s, must, store.NewIntent{AgentID: agent, Kind: "reply", Text: text, TargetURL: fmt.Sprintf("https://www.threads.com/@%s/post/DQ%05d", user, 500+idx*13), TargetUsername: user, At: t.Add(4 * time.Minute)}, fmt.Sprintf("https://www.threads.com/@%s/post/DR%05d", username, 900+idx*5), 100+idx)
				do("reply")
				base = 90
				switch {
				case viralReply[idx]:
					base = 1800
				case flopReply[idx]:
					base = 8
				}
				results[id] = pickResult(rng, base, 0.15, viralReply[idx], flopReply[idx])
				events = append(events, event{at: t.Add(4 * time.Minute), kind: "reply", action: id})
			}
			if blockedCount < len(blocked) && i > 0 && i%17 == 5 {
				step("reply", "", "", blocked[blockedCount])
				blockedCount++
				steps++
			}
			base = 0.04 + rng.Float64()*0.045
		case "dream":
			do("read_replies")
			base = 0.035 + rng.Float64()*0.015
		}
		end := t.Add(3 * time.Minute)
		must(s.RecordLLMUsage(ctx, agent, wake, sl.at.Format("2006-01-02"), store.LLMUsage{TokensIn: int64(base * 1.6e6), TokensOut: int64(base * 2.2e5), CostUSD: base}))
		must(s.FinishWake(ctx, wake, end, steps, "completed"))
	}
	if blockedCount < len(blocked) {
		return fmt.Errorf("only %d blocked attempts seeded", blockedCount)
	}

	// Measure through the store's own job queue, replaying each due window in order.
	_, err = s.SeedMetricJobs(ctx, agent, now)
	must(err)
	type due struct {
		at     time.Time
		action int64
		window int
	}
	var dues []due
	posted := map[int64]time.Time{}
	for _, e := range events {
		posted[e.action] = e.at
		for _, w := range []int{1, 6, 24} {
			dues = append(dues, due{e.at.Add(time.Duration(w) * time.Hour), e.action, w})
		}
	}
	sort.Slice(dues, func(i, j int) bool { return dues[i].at.Before(dues[j].at) })
	share := map[int]float64{1: 0.28, 6: 0.65, 24: 1}
	for _, d := range dues {
		at := d.at.Add(time.Minute)
		if at.After(now) {
			continue
		}
		jobs, err := s.ClaimMetricJobs(ctx, agent, at, 5*time.Minute, 1000)
		must(err)
		if len(jobs) == 0 || jobs[0].ActionID != d.action {
			return fmt.Errorf("metric replay out of order at action %d", d.action)
		}
		r, f := results[d.action], followersAt(at)
		x := share[jobs[len(jobs)-1].WindowHours]
		sc := func(v int64) int64 { return int64(float64(v) * x) }
		_, err = s.CompleteMetricJobs(ctx, jobs, at, store.MetricValues{Views: sc(r.views), Likes: sc(r.likes), Replies: sc(r.replies), Reposts: sc(r.reposts), Followers: &f})
		must(err)
	}
	must(s.RecordFollowers(ctx, agent, followersAt(now), now))
	must(s.SetNextWake(ctx, agent, now.Add(2*time.Hour)))
	return nil
}

func pickResult(rng *rand.Rand, base, engage float64, viral, flop bool) result {
	views := int64(base * (0.7 + rng.Float64()*0.6))
	rate := 0.045 + rng.Float64()*0.02
	r := result{views: views}
	switch {
	case viral:
		rate = 0.09
	case flop:
		rate = 0.01
	}
	r.likes = int64(float64(views) * rate)
	r.replies = int64(float64(views) * rate * 0.22)
	r.reposts = int64(float64(views) * rate * 0.1 * (1 + engage))
	return r
}

func publish(ctx context.Context, s *store.Store, must func(error), in store.NewIntent, url string, n int) int64 {
	var id int64
	must(s.Reserve(ctx, func(tx store.ReservationTx) error {
		var err error
		id, err = tx.InsertIntent(ctx, in)
		return err
	}))
	ok, err := s.ConfirmPublished(ctx, in.AgentID, id, url, fmt.Sprintf("1790%06d", n))
	must(err)
	if !ok {
		log.Fatalf("action %d not confirmed", id)
	}
	return id
}
