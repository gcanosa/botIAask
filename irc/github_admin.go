package irc

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"botIAask/config"
	"botIAask/github"
	"botIAask/rss"

	"github.com/ergochat/irc-go/ircmsg"
)

// ghTokenTimeout bounds how long an admin has to reply with a PAT after !gh add --private
// asks for one in a PM.
const ghTokenTimeout = 120 * time.Second

// whoisSecureTimeout bounds how long !gh add --private waits for RPL_WHOISSECURE (671)
// before concluding the connection can't be verified as secure.
const whoisSecureTimeout = 5 * time.Second

var (
	ghNumericQueryRe = regexp.MustCompile(`^#?\d+$`)
	ghHexQueryRe     = regexp.MustCompile(`(?i)^[0-9a-f]{7,40}$`)
)

// pendingGHTokenRequest tracks an in-flight "send me your PAT in this PM" request, keyed
// like adminSessionKey(network, nick) in Bot.pendingGHTokens.
type pendingGHTokenRequest struct {
	owner, repo string
	channels    []string
	deadline    time.Time
	cancel      func() // stops the expiry timer if the token arrives first
}

// SetGitHubFetcher wires the GitHub tracker fetcher into the bot so !gh commands can read
// tracked-repo status, encrypt/decrypt tokens, and run one-shot search lookups.
func (b *Bot) SetGitHubFetcher(f *github.Fetcher) {
	b.githubFetcher = f
}

// checkWhoisSecure issues a WHOIS for nick and reports whether the server confirmed a
// secure connection (numeric 671, RPL_WHOISSECURE) before whoisSecureTimeout or
// RPL_ENDOFWHOIS (318) arrives, whichever is first. Not every ircd sends 671 even over
// TLS (it's opt-in server support) — a false result here means "couldn't verify", not
// "definitely insecure", so callers must treat it as fail-closed (refuse the token flow),
// not as proof the connection is actually plaintext.
func (b *ircNetwork) checkWhoisSecure(nick string) bool {
	resultCh := make(chan bool, 1)
	var once sync.Once
	send := func(v bool) { once.Do(func() { resultCh <- v }) }

	secureID := b.conn.AddCallback("671", func(e ircmsg.Message) {
		if len(e.Params) >= 2 && strings.EqualFold(e.Params[1], nick) {
			send(true)
		}
	})
	endID := b.conn.AddCallback("318", func(e ircmsg.Message) {
		if len(e.Params) >= 2 && strings.EqualFold(e.Params[1], nick) {
			send(false)
		}
	})
	defer b.conn.RemoveCallback(secureID)
	defer b.conn.RemoveCallback(endID)

	if err := b.conn.Send("WHOIS", nick); err != nil {
		return false
	}
	select {
	case v := <-resultCh:
		return v
	case <-time.After(whoisSecureTimeout):
		return false
	}
}

// tryConsumePendingGitHubToken checks whether this PRIVMSG is the admin's reply to a
// pending !gh add --private token request; if so it handles it (success, expiry, or a
// wrong-context reply) and reports true so the caller skips normal logging/dispatch for
// this message — a raw PAT must never reach logs/, !seen, or !tell.
func (b *ircNetwork) tryConsumePendingGitHubToken(target, message, sender string) bool {
	key := adminSessionKey(b.name, sender)
	b.ghTokenMu.Lock()
	req, ok := b.pendingGHTokens[key]
	if ok {
		delete(b.pendingGHTokens, key)
	}
	b.ghTokenMu.Unlock()
	if !ok {
		return false
	}
	req.cancel()

	if ircChannelTarget(target) {
		b.sendNotice(sender, "Private-repo tokens can only be sent in a PM — run !gh add ... --private again and reply here.")
		return true
	}
	if time.Now().After(req.deadline) {
		b.sendNotice(sender, "Token request expired. Run !gh add again.")
		return true
	}
	token := strings.TrimSpace(message)
	if token == "" {
		b.sendNotice(sender, "Empty token received. Run !gh add again.")
		return true
	}

	encrypted, err := b.githubFetcher.EncryptToken(token)
	if err != nil {
		b.sendNotice(sender, fmt.Sprintf("Failed to encrypt token: %v", err))
		return true
	}
	if err := b.commitGHRepoAdd(sender, req.owner, req.repo, req.channels, encrypted); err != nil {
		b.sendNotice(sender, fmt.Sprintf("Failed to add %s/%s: %v", req.owner, req.repo, err))
		return true
	}
	b.sendNotice(sender, fmt.Sprintf("Tracking %s/%s (private, token stored encrypted). Channels: %s",
		req.owner, req.repo, strings.Join(req.channels, ", ")))
	return true
}

// handleGHCommand dispatches "!gh list|add|del|search" subcommands. Admin gating happens
// in the caller (irc/bot.go), before this is ever reached.
func (b *ircNetwork) handleGHCommand(target, message, sender, source string) {
	body := strings.TrimSpace(strings.TrimPrefix(message, b.pfx()+"gh"))
	sub, rest := splitFirstWord(body)
	switch strings.ToUpper(sub) {
	case "LIST":
		b.ghList(target)
	case "ADD":
		b.ghAdd(target, sender, rest)
	case "DEL", "REMOVE":
		b.ghDel(target, rest)
	case "SEARCH":
		b.ghSearch(target, rest)
	default:
		b.sendPrivmsg(target, fmt.Sprintf(
			"Usage: %sgh list | %sgh add <owner>/<repo> [network:#chan ...] [--private] | %sgh del <owner>/<repo> | %sgh search <owner>/<repo> <query>",
			b.pfx(), b.pfx(), b.pfx(), b.pfx()))
	}
}

// splitFirstWord splits s into its first whitespace-delimited word and the (trimmed)
// remainder, matching the inline convention !bookmark already uses.
func splitFirstWord(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

func splitOwnerRepo(s string) (owner, repo string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (b *ircNetwork) ghList(target string) {
	repos := b.getCfg().GitHubTracker.Repos
	if len(repos) == 0 {
		b.sendPrivmsg(target, "No GitHub repos tracked.")
		return
	}
	status := map[string]github.RepoStatus{}
	if b.githubFetcher != nil {
		for _, st := range b.githubFetcher.RepoStatuses() {
			status[strings.ToLower(st.Repo)] = st
		}
	}
	b.sendPrivmsg(target, fmt.Sprintf("Tracking %d repo(s):", len(repos)))
	for _, r := range repos {
		tok := "no token"
		if r.TokenEncrypted != "" {
			tok = "token set"
		}
		events := "all"
		if len(r.EventTypes) > 0 {
			events = strings.Join(r.EventTypes, ",")
		}
		state := ""
		if st, ok := status[strings.ToLower(r.FullName())]; ok {
			if st.OK {
				state = " ok"
			} else if st.Error != "" {
				state = " error: " + st.Error
			}
		}
		b.sendPrivmsg(target, fmt.Sprintf("- %s  channels=%s  events=%s  %s%s",
			r.FullName(), strings.Join(r.Channels, ","), events, tok, state))
		time.Sleep(500 * time.Millisecond)
	}
}

func (b *ircNetwork) ghAdd(target, sender, rest string) {
	fields := strings.Fields(rest)
	private := false
	var kept []string
	for _, f := range fields {
		if f == "--private" {
			private = true
			continue
		}
		kept = append(kept, f)
	}
	if len(kept) < 1 {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %sgh add <owner>/<repo> [network:#chan ...] [--private]", b.pfx()))
		return
	}
	owner, repo, ok := splitOwnerRepo(kept[0])
	if !ok {
		b.sendPrivmsg(target, "Usage: owner/repo, e.g. anthropics/claude-code")
		return
	}
	channels := kept[1:]
	if _, _, exists := config.FindGitHubTrackerRepo(b.getCfg().GitHubTracker.Repos, owner, repo); exists {
		b.sendPrivmsg(target, fmt.Sprintf("%s/%s is already tracked.", owner, repo))
		return
	}
	if b.githubFetcher == nil {
		b.sendPrivmsg(target, "GitHub tracker not initialized.")
		return
	}

	if !private {
		if err := b.commitGHRepoAdd(sender, owner, repo, channels, ""); err != nil {
			b.sendPrivmsg(target, fmt.Sprintf("Failed to add %s/%s: %v", owner, repo, err))
			return
		}
		b.sendPrivmsg(target, fmt.Sprintf("Tracking %s/%s (public). Channels: %s", owner, repo, strings.Join(channels, ", ")))
		return
	}

	// --private path: never accept a raw PAT typed in a channel, and only offer the PM
	// flow when the admin's own connection can be verified encrypted (fail closed).
	if ircChannelTarget(target) {
		b.sendPrivmsg(target, fmt.Sprintf("@%s: Private-repo tokens can only be added in a PM to me — resend this in a query.", sender))
		return
	}
	if !b.checkWhoisSecure(sender) {
		b.sendNotice(sender, "Can't verify this connection is encrypted (no secure-connection confirmation from the server within 5s). Add private-repo tokens via the web dashboard instead.")
		return
	}

	key := adminSessionKey(b.name, sender)
	deadline := time.Now().Add(ghTokenTimeout)
	timer := time.AfterFunc(ghTokenTimeout, func() {
		b.ghTokenMu.Lock()
		_, stillPending := b.pendingGHTokens[key]
		if stillPending {
			delete(b.pendingGHTokens, key)
		}
		b.ghTokenMu.Unlock()
		if stillPending {
			b.sendNotice(sender, fmt.Sprintf("Timed out waiting for a token for %s/%s. Run !gh add again.", owner, repo))
		}
	})
	b.ghTokenMu.Lock()
	b.pendingGHTokens[key] = pendingGHTokenRequest{
		owner: owner, repo: repo, channels: channels, deadline: deadline,
		cancel: func() { timer.Stop() },
	}
	b.ghTokenMu.Unlock()
	b.sendNotice(sender, fmt.Sprintf("Reply in this PM with the PAT for %s/%s within 120s. It will not be echoed or logged.", owner, repo))
}

// commitGHRepoAdd clones the live config, appends the new repo entry, validates, saves to
// disk, and rehashes — the same disk-based path the web dashboard's add handler uses,
// required because github.Fetcher holds its own config pointer separate from irc.Bot's.
func (b *ircNetwork) commitGHRepoAdd(sender, owner, repo string, channels []string, tokenEncrypted string) error {
	clone, err := config.CloneConfig(b.getCfg())
	if err != nil {
		return err
	}
	clone.GitHubTracker.Repos = append(clone.GitHubTracker.Repos, config.GitHubTrackerRepoConfig{
		Owner: owner, Repo: repo, Channels: channels, TokenEncrypted: tokenEncrypted,
	})
	if err := config.ValidateConfig(clone); err != nil {
		return err
	}
	if err := config.SaveConfig(configPathOrDefault(b.Bot), clone); err != nil {
		return err
	}
	if err := b.RunRehash(fmt.Sprintf("irc admin %s (!gh add)", sender)); err != nil {
		return err
	}
	b.joinGHChannels(channels)
	return nil
}

// joinGHChannels best-effort live-joins each "network:#chan" entry for this process only
// (not persisted), mirroring web/github_tracker.go's joinAnnounceChannels.
func (b *ircNetwork) joinGHChannels(channels []string) {
	for _, raw := range channels {
		network, ch := config.SplitNetworkChannel(raw, b.name)
		ch = strings.TrimSpace(ch)
		if network == "" || ch == "" {
			continue
		}
		if err := b.JoinChannelSession(network, config.IRChannel{Name: ch}); err != nil {
			log.Printf("[GITHUB] !gh add: join %s on %s: %v", ch, network, err)
		}
	}
}

func (b *ircNetwork) ghDel(target, rest string) {
	owner, repo, ok := splitOwnerRepo(rest)
	if !ok {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %sgh del <owner>/<repo>", b.pfx()))
		return
	}
	clone, err := config.CloneConfig(b.getCfg())
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Failed to remove %s/%s: %v", owner, repo, err))
		return
	}
	if _, _, exists := config.FindGitHubTrackerRepo(clone.GitHubTracker.Repos, owner, repo); !exists {
		b.sendPrivmsg(target, fmt.Sprintf("%s/%s is not tracked.", owner, repo))
		return
	}
	var out []config.GitHubTrackerRepoConfig
	for _, r := range clone.GitHubTracker.Repos {
		if !(strings.EqualFold(r.Owner, owner) && strings.EqualFold(r.Repo, repo)) {
			out = append(out, r)
		}
	}
	clone.GitHubTracker.Repos = out
	if err := config.SaveConfig(configPathOrDefault(b.Bot), clone); err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Failed to remove %s/%s: %v", owner, repo, err))
		return
	}
	if err := b.RunRehash("!gh del"); err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Removed but rehash failed: %v", err))
		return
	}
	b.sendPrivmsg(target, fmt.Sprintf("Stopped tracking %s/%s.", owner, repo))
}

func (b *ircNetwork) ghSearch(target, rest string) {
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %sgh search <owner>/<repo> <query>", b.pfx()))
		return
	}
	owner, repo, ok := splitOwnerRepo(fields[0])
	if !ok {
		b.sendPrivmsg(target, "Usage: owner/repo, e.g. anthropics/claude-code")
		return
	}
	if b.githubFetcher == nil {
		b.sendPrivmsg(target, "GitHub tracker not initialized.")
		return
	}
	repoCfg, _, ok := config.FindGitHubTrackerRepo(b.getCfg().GitHubTracker.Repos, owner, repo)
	if !ok {
		b.sendPrivmsg(target, fmt.Sprintf("%s/%s is not a tracked repo.", owner, repo))
		return
	}
	token, err := b.githubFetcher.DecryptToken(repoCfg.TokenEncrypted)
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Failed to decrypt stored token: %v", err))
		return
	}
	query := strings.Join(fields[1:], " ")
	shortener := b.getCfg().RSS.URLShortener

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch {
	case ghNumericQueryRe.MatchString(query):
		b.ghSearchByNumber(ctx, target, owner, repo, token, query, shortener)
	case ghHexQueryRe.MatchString(query):
		b.ghSearchByCommit(ctx, target, owner, repo, token, query, shortener)
	default:
		b.ghSearchLocal(target, repoCfg.FullName(), query)
	}
}

func (b *ircNetwork) ghSearchByNumber(ctx context.Context, target, owner, repo, token, query, shortener string) {
	n, _ := strconv.Atoi(strings.TrimPrefix(query, "#"))
	pr, err := github.FetchPullRequest(ctx, owner, repo, token, n)
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Error looking up #%d: %v", n, err))
		return
	}
	if pr != nil {
		link := rss.ShortenURLWithService(pr.HTMLURL, shortener)
		b.sendPrivmsg(target, fmt.Sprintf("[PR #%d] %s (%s) by %s 🔗 %s", pr.Number, pr.Title, pr.State, pr.Author, link))
		return
	}
	issue, err := github.FetchIssue(ctx, owner, repo, token, n)
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Error looking up #%d: %v", n, err))
		return
	}
	if issue != nil {
		link := rss.ShortenURLWithService(issue.HTMLURL, shortener)
		b.sendPrivmsg(target, fmt.Sprintf("[ISSUE #%d] %s (%s) by %s 🔗 %s", issue.Number, issue.Title, issue.State, issue.Author, link))
		return
	}
	b.sendPrivmsg(target, fmt.Sprintf("No PR or issue #%d found in %s/%s.", n, owner, repo))
}

func (b *ircNetwork) ghSearchByCommit(ctx context.Context, target, owner, repo, token, sha, shortener string) {
	commit, err := github.FetchCommit(ctx, owner, repo, token, sha)
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Error looking up commit %s: %v", sha, err))
		return
	}
	if commit == nil {
		b.sendPrivmsg(target, fmt.Sprintf("No commit %s found in %s/%s.", sha, owner, repo))
		return
	}
	short := commit.SHA
	if len(short) > 7 {
		short = short[:7]
	}
	link := rss.ShortenURLWithService(commit.HTMLURL, shortener)
	b.sendPrivmsg(target, fmt.Sprintf("[COMMIT %s] %s by %s 🔗 %s", short, commit.Message, commit.Author, link))
}

func (b *ircNetwork) ghSearchLocal(target, repoKey, query string) {
	pattern, err := regexp.Compile("(?i)" + query)
	if err != nil {
		pattern = regexp.MustCompile("(?i)" + regexp.QuoteMeta(query))
	}
	rows, err := b.githubFetcher.DB().SearchEvents(repoKey, pattern, 5)
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Search error: %v", err))
		return
	}
	if len(rows) == 0 {
		b.sendPrivmsg(target, "No matches.")
		return
	}
	for _, row := range rows {
		b.sendPrivmsg(target, fmt.Sprintf("- [%s] %s", row.RefID, row.Message))
		time.Sleep(500 * time.Millisecond)
	}
}
