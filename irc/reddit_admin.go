package irc

import (
	"fmt"
	"log"
	"slices"
	"strings"

	"botIAask/config"
	"botIAask/reddit"
)

// SetRedditFetcher wires the subreddit announcer in (for !reddit and the web dashboard).
func (b *Bot) SetRedditFetcher(f *reddit.Fetcher) { b.redditFetcher = f }

// RedditFetcher returns the subreddit announcer, or nil if it isn't set up.
func (b *Bot) RedditFetcher() *reddit.Fetcher { return b.redditFetcher }

// AllowRedditSub refuses an NSFW subreddit unless nsfw is set. Reddit blocking the check
// is not a reason to refuse: the add goes through and the failure is logged.
func AllowRedditSub(f *reddit.Fetcher, name string, nsfw bool) error {
	if nsfw || f == nil {
		return nil
	}
	over18, err := f.CheckNSFW(name)
	if err != nil {
		log.Printf("[REDDIT] NSFW check for r/%s failed, allowing: %v", name, err)
		return nil
	}
	if over18 {
		return fmt.Errorf("r/%s is NSFW; add it with NSFW allowed", name)
	}
	return nil
}

// handleRedditCommand: !reddit list | add <sub> [#chan|network:#chan ...] [--nsfw] | del <sub> [channels...]
// Admin + !admin session are checked by the caller (bot.go).
func (b *ircNetwork) handleRedditCommand(target, sender string, args []string) {
	usage := fmt.Sprintf("Usage: %sreddit list | %sreddit add <sub> [#chan|network:#chan ...] [--nsfw] | %sreddit del <sub> [channels...]", b.pfx(), b.pfx(), b.pfx())
	if len(args) == 0 {
		b.sendPrivmsg(target, usage)
		return
	}
	if strings.EqualFold(args[0], "list") {
		b.redditList(target)
		return
	}
	add := strings.EqualFold(args[0], "add")
	if len(args) < 2 || (!add && !strings.EqualFold(args[0], "del") && !strings.EqualFold(args[0], "remove")) {
		b.sendPrivmsg(target, usage)
		return
	}
	name, ok := config.NormalizeRedditName(args[1])
	if !ok {
		b.sendPrivmsg(target, "Invalid subreddit name (e.g. golang or r/golang).")
		return
	}
	nsfw := false
	var channels []string
	for _, a := range args[2:] {
		if a == "--nsfw" {
			nsfw = true
			continue
		}
		if !strings.Contains(a, ":") { // bare #chan = this network
			a = b.name + ":" + a
		}
		channels = append(channels, a)
	}
	if add && len(channels) == 0 {
		if !ircChannelTarget(target) {
			b.sendPrivmsg(target, "Name at least one channel, e.g. "+b.pfx()+"reddit add "+name+" #chan")
			return
		}
		channels = []string{b.name + ":" + target}
	}

	if add {
		b.redditAdd(target, sender, name, channels, nsfw)
		return
	}
	b.redditDel(target, sender, name, channels)
}

func (b *ircNetwork) redditAdd(target, sender, name string, channels []string, nsfw bool) {
	exists := config.FindRedditSub(b.getCfg().Reddit.Subreddits, name) >= 0
	if !exists {
		if err := AllowRedditSub(b.redditFetcher, name, nsfw); err != nil {
			b.sendPrivmsg(target, err.Error()+" (--nsfw).")
			return
		}
	}
	err := b.editRedditSubs(fmt.Sprintf("irc admin %s (!reddit add)", sender), func(subs *[]config.RedditSub) {
		i := config.FindRedditSub(*subs, name)
		if i < 0 {
			*subs = append(*subs, config.RedditSub{Name: name, NSFW: nsfw})
			i = len(*subs) - 1
		}
		s := &(*subs)[i]
		s.NSFW = s.NSFW || nsfw
		for _, c := range channels {
			if !slices.ContainsFunc(s.Channels, func(o string) bool { return strings.EqualFold(o, c) }) {
				s.Channels = append(s.Channels, c)
			}
		}
	})
	if err != nil {
		b.sendPrivmsg(target, "reddit: "+err.Error())
		return
	}
	b.joinGHChannels(channels)
	b.sendPrivmsg(target, fmt.Sprintf("Following r/%s in %s.", name, strings.Join(channels, ", ")))
}

func (b *ircNetwork) redditDel(target, sender, name string, channels []string) {
	if config.FindRedditSub(b.getCfg().Reddit.Subreddits, name) < 0 {
		b.sendPrivmsg(target, fmt.Sprintf("r/%s is not followed.", name))
		return
	}
	err := b.editRedditSubs(fmt.Sprintf("irc admin %s (!reddit del)", sender), func(subs *[]config.RedditSub) {
		i := config.FindRedditSub(*subs, name)
		if len(channels) == 0 {
			*subs = slices.Delete(*subs, i, i+1)
			return
		}
		(*subs)[i].Channels = slices.DeleteFunc((*subs)[i].Channels, func(o string) bool {
			return slices.ContainsFunc(channels, func(c string) bool { return strings.EqualFold(o, c) })
		})
	})
	if err != nil {
		b.sendPrivmsg(target, "reddit: "+err.Error())
		return
	}
	if len(channels) == 0 {
		b.sendPrivmsg(target, fmt.Sprintf("Stopped following r/%s.", name))
		return
	}
	b.sendPrivmsg(target, fmt.Sprintf("r/%s no longer announced in %s.", name, strings.Join(channels, ", ")))
}

func (b *ircNetwork) redditList(target string) {
	subs := b.getCfg().Reddit.Subreddits
	if len(subs) == 0 {
		b.sendPrivmsg(target, "No subreddits followed.")
		return
	}
	status := map[string]reddit.SubStatus{}
	if b.redditFetcher != nil {
		for _, st := range b.redditFetcher.Statuses() {
			status[strings.ToLower(st.Name)] = st
		}
	}
	b.sendPrivmsg(target, fmt.Sprintf("Following %d subreddit(s):", len(subs)))
	for i, s := range subs {
		if i == maxListLines {
			b.sendPrivmsg(target, fmt.Sprintf("... and %d more (see the dashboard).", len(subs)-i))
			break
		}
		state := ""
		if st, ok := status[strings.ToLower(s.Name)]; ok && st.Error != "" {
			state = " error: " + st.Error
		}
		nsfw := ""
		if s.NSFW {
			nsfw = " nsfw"
		}
		chans := strings.Join(s.Channels, ",")
		if chans == "" {
			chans = "none"
		}
		b.sendPrivmsg(target, fmt.Sprintf("- r/%s  channels=%s%s%s", s.Name, chans, nsfw, state))
	}
}

// editRedditSubs applies fn to a config clone's subreddit list, saves it and rehashes.
func (b *ircNetwork) editRedditSubs(source string, fn func(subs *[]config.RedditSub)) error {
	clone, err := config.CloneConfig(b.getCfg())
	if err != nil {
		return err
	}
	fn(&clone.Reddit.Subreddits)
	if err := config.SaveConfig(configPathOrDefault(b.Bot), clone); err != nil {
		return err
	}
	if err := b.RunRehash(source); err != nil {
		return fmt.Errorf("saved, but rehash failed: %w", err)
	}
	return nil
}
