package reddit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"botIAask/config"
)

type fakeBot struct {
	mu        sync.Mutex
	connected bool
	lines     []string
}

func (b *fakeBot) Broadcast(ch []string, msg string) {
	b.mu.Lock()
	b.lines = append(b.lines, strings.Join(ch, ",")+" "+msg)
	b.mu.Unlock()
}
func (b *fakeBot) IsConnected() bool { return b.connected }

// fakeReddit serves an Atom feed per sub; posts[sub] is a list of ids, minute = publish order.
type fakeReddit struct {
	mu     sync.Mutex
	posts  map[string][]int // id n => published at base + n minutes
	status int
}

func (r *fakeReddit) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != 0 {
		w.WriteHeader(r.status)
		return
	}
	if strings.HasSuffix(req.URL.Path, "/about.json") {
		fmt.Fprint(w, `{"data":{"over18":true}}`)
		return
	}
	sub := strings.Split(req.URL.Path, "/")[2]
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>r</title>`)
	for _, n := range r.posts[sub] {
		fmt.Fprintf(w, `<entry><id>t3_%s%d</id><title>%s post %d &amp; more</title><author><name>/u/bob</name></author><link href="https://www.reddit.com/r/%s/comments/x/"/><published>%s</published><updated>%s</updated></entry>`,
			sub, n, sub, n, sub, base.Add(time.Duration(n)*time.Minute).Format(time.RFC3339), base.Format(time.RFC3339))
	}
	fmt.Fprint(w, `</feed>`)
}

func setup(t *testing.T, subs ...config.RedditSub) (*Fetcher, *fakeBot, *fakeReddit) {
	t.Helper()
	d, err := NewDatabase(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	fr := &fakeReddit{posts: map[string][]int{}}
	srv := httptest.NewServer(fr)
	t.Cleanup(srv.Close)
	bot := &fakeBot{connected: true}
	f := NewFetcher(&config.Config{Reddit: config.RedditConfig{Subreddits: subs}}, bot, d)
	f.baseURL, f.gap = srv.URL, 0
	return f, bot, fr
}

func (b *fakeBot) take() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.lines
	b.lines = nil
	return out
}

func TestFetchDedupOrderAndCaps(t *testing.T) {
	f, bot, fr := setup(t,
		config.RedditSub{Name: "a", Channels: []string{"net:#a"}},
		config.RedditSub{Name: "b", Channels: []string{"net:#b"}})
	fr.posts["a"] = []int{1, 2, 3, 4, 5}
	fr.posts["b"] = []int{6}

	// First poll: newest 3 of a + the only post of b, oldest first across subs.
	f.Fetch()
	got := bot.take()
	want := []string{"a post 3", "a post 4", "a post 5", "b post 6"}
	if len(got) != len(want) {
		t.Fatalf("first poll: %q", got)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w+" & more") {
			t.Fatalf("line %d = %q, want %q", i, got[i], w)
		}
	}
	if !strings.HasPrefix(got[0], "net:#a ") || !strings.HasPrefix(got[3], "net:#b ") || !strings.Contains(got[0], "https://redd.it/a3") || !strings.Contains(got[0], "(u/bob)") {
		t.Fatalf("routing/format: %q", got)
	}

	// Nothing new: nothing announced.
	f.Fetch()
	if got := bot.take(); len(got) != 0 {
		t.Fatalf("repeat: %q", got)
	}

	// 12 new posts: 10 now, 2 next cycle.
	for n := 10; n < 22; n++ {
		fr.posts["a"] = append(fr.posts["a"], n)
	}
	f.Fetch()
	if got := bot.take(); len(got) != maxAnnouncePerCycle || !strings.Contains(got[0], "a post 10 ") {
		t.Fatalf("cap: %d %q", len(got), got)
	}
	f.Fetch()
	if got := bot.take(); len(got) != 2 || !strings.Contains(got[1], "a post 21 ") {
		t.Fatalf("remainder: %q", got)
	}

	// IRC down: nothing marked seen.
	fr.posts["b"] = append(fr.posts["b"], 30)
	bot.connected = false
	f.Fetch()
	bot.connected = true
	f.Fetch()
	if got := bot.take(); len(got) != 1 || !strings.Contains(got[0], "b post 30") {
		t.Fatalf("after reconnect: %q", got)
	}
}

func TestFetchBlockedStopsCycle(t *testing.T) {
	f, bot, fr := setup(t, config.RedditSub{Name: "a"}, config.RedditSub{Name: "b"})
	fr.status = http.StatusTooManyRequests
	f.Fetch()
	st := f.Statuses()
	if st[0].OK || !strings.Contains(st[0].Error, "rate limited") || st[1].Error != "not yet polled" {
		t.Fatalf("statuses: %+v", st)
	}
	if len(bot.take()) != 0 {
		t.Fatal("announced while blocked")
	}
}

func TestCheckNSFW(t *testing.T) {
	f, _, _ := setup(t)
	if over, err := f.CheckNSFW("x"); err != nil || !over {
		t.Fatalf("over18=%v err=%v", over, err)
	}
}

func TestFormatStripsControlChars(t *testing.T) {
	line := FormatLine(Post{Sub: "go", Title: "evil\r\nPRIVMSG #x :hi\x03", Author: "a\x02b", Link: "https://redd.it/1"})
	if strings.ContainsAny(line, "\r\n\x02") || strings.Contains(line, "hi\x03") {
		t.Fatalf("unsafe line %q", line)
	}
}
