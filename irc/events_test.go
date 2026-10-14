package irc

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"botIAask/bookmarks"
	"botIAask/config"
	"botIAask/logger"
	"botIAask/progtodo"
	"botIAask/stats"

	"github.com/ergochat/irc-go/ircmsg"
)

// newEventNetwork builds a real (never connected) network so the registered IRC callbacks can be
// driven with HandleMessage; output is captured and pacing is off.
func newEventNetwork(t *testing.T) (*ircNetwork, *sink) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Cleanup(logger.CloseAll)
	cfg := &config.Config{}
	cfg.Bot.CommandPrefix = "!"
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "test", Server: "irc.example.org", Port: 6667, Nickname: "bot"}}
	b := newTestBot(cfg)
	n := b.buildNetwork(cfg.IRC.Networks[0])
	s := &sink{}
	n.testSend = s.add
	n.pace = pacer{burst: 1 << 20, interval: time.Nanosecond}
	return n, s
}

func fire(n *ircNetwork, source, command string, params ...string) {
	n.conn.HandleMessage(ircmsg.MakeMessage(nil, source, command, params...))
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func memberCount(n *ircNetwork, ch string) (count int, present bool) {
	n.membersMu.RLock()
	defer n.membersMu.RUnlock()
	m, ok := n.channelMembers[ch]
	return len(m), ok
}

func TestMembershipResetOnDisconnectAndOwnPart(t *testing.T) {
	n, _ := newEventNetwork(t)
	fire(n, "alice!u@h", "JOIN", "#c")
	fire(n, "bob!u@h", "JOIN", "#c")
	fire(n, "carol!u@h", "JOIN", "#d")
	if c, _ := memberCount(n, "#c"); c != 2 {
		t.Fatalf("#c members = %d, want 2", c)
	}

	fire(n, "bot!u@h", "PART", "#d") // we left #d
	if _, present := memberCount(n, "#d"); present {
		t.Fatal("channel entry kept after our own PART")
	}
	fire(n, "op!u@h", "KICK", "#c", "bot") // we were kicked from #c
	if _, present := memberCount(n, "#c"); present {
		t.Fatal("channel entry kept after we were kicked")
	}

	fire(n, "alice!u@h", "JOIN", "#e")
	fire(n, "", "\x00DISCONNECT")
	if _, present := memberCount(n, "#e"); present {
		t.Fatal("membership survived a disconnect")
	}
}

func TestTrackerRefreshOnlyForLoggedInAdmins(t *testing.T) {
	n, _ := newEventNetwork(t)
	n.tracker = stats.NewTracker(n.getCfg(), nil)
	fire(n, "Alice!u@h", "JOIN", "#c") // joins before she logs in; case differs from the session key
	n.loggedInAdmins[adminSessionKey("test", "alice")] = true

	// bob is not an admin: no refresh, tracker still knows nothing
	n.refreshAdminsFor("bob")
	if nicks, _ := n.tracker.GetAdmins(); len(nicks) != 0 {
		t.Fatalf("refresh ran for a non-admin: %v", nicks)
	}
	n.refreshAdminsFor("alice")
	nicks, chans := n.tracker.GetAdmins()
	if len(nicks) != 1 || nicks[0] != "alice" {
		t.Fatalf("admin nicks = %v", nicks)
	}
	if got := chans["test:#c"]; len(got) != 1 || got[0] != "Alice" {
		t.Fatalf("channel presence = %v (want the member's real-case nick)", chans)
	}
}

func TestJoinDeliversCappedRemindersOffTheReadLoop(t *testing.T) {
	n, out := newEventNetwork(t)
	db, err := bookmarks.NewDatabase(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n.bookmarksDB = db
	for i := 0; i < 8; i++ {
		if _, err := db.AddReminder("test", "alice", fmt.Sprintf("note %d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	fire(n, "alice!u@h", "JOIN", "#c")
	waitFor(t, "join reminders", func() bool { return len(out.all()) >= maxJoinReminders+1 })
	lines := out.all()
	if len(lines) != maxJoinReminders+1 || !strings.Contains(lines[len(lines)-1], "(+3 more reminders") {
		t.Fatalf("want %d reminders + overflow line, got %q", maxJoinReminders, lines)
	}
	if !strings.HasPrefix(lines[0], "NOTICE alice ") {
		t.Fatalf("reminders must go to the owner by NOTICE: %q", lines[0])
	}
}

func TestPendingTellsDeliveredAsynchronously(t *testing.T) {
	n, out := newEventNetwork(t)
	db, err := bookmarks.NewDatabase(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n.bookmarksDB = db
	if _, err := db.AddTell("test", "bob", "alice", "hi there"); err != nil {
		t.Fatal(err)
	}
	n.markPendingTell("test", "alice")
	fire(n, "alice!u@h", "PRIVMSG", "#c", "hello")
	waitFor(t, "tell delivery", func() bool { return len(out.all()) >= 1 })
	if got := out.all()[0]; !strings.Contains(got, "bob left a message") || !strings.Contains(got, "hi there") {
		t.Fatalf("tell not delivered: %q", got)
	}
}

func TestTodoAndReminderCapsAndListLength(t *testing.T) {
	n, out := newEventNetwork(t)
	bdb, err := bookmarks.NewDatabase(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()
	pdb, err := progtodo.NewDatabase(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer pdb.Close()
	n.bookmarksDB, n.progtodoDB = bdb, pdb

	for i := 0; i < maxTodosPerNick; i++ {
		if _, err := pdb.Add(fmt.Sprintf("todo %d", i), "alice", "test", false, ""); err != nil {
			t.Fatal(err)
		}
	}
	n.handleCommand("#c", "!todo add one too many", "alice", "alice!u@h")
	if got := out.all(); len(got) != 1 || !strings.Contains(got[0], "already have") {
		t.Fatalf("todo cap not enforced: %q", got)
	}
	n.handleCommand("#c", "!todo list", "alice", "alice!u@h")
	if got := out.all(); len(got)-1 != maxListLines+1 || !strings.Contains(got[len(got)-1], "(+15 more") {
		t.Fatalf("todo list should print %d lines plus an overflow note, got %d lines: %q", maxListLines, len(got)-1, got)
	}

	for i := 0; i < maxRemindersPerNick; i++ {
		if _, err := bdb.AddReminder("test", "alice", fmt.Sprintf("r%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	before := len(out.all())
	n.handleCommand("#c", "!reminder add 0 one too many", "alice", "alice!u@h")
	if got := out.all(); len(got) != before+1 || !strings.Contains(got[before], "already have") {
		t.Fatalf("reminder cap not enforced: %q", got[before:])
	}
	before = len(out.all())
	n.handleCommand("#c", "!reminder list", "alice", "alice!u@h")
	if got := out.all(); len(got)-before != maxListLines+1 {
		t.Fatalf("reminder list printed %d lines, want %d", len(got)-before, maxListLines+1)
	}
}
