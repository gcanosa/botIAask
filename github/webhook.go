package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"botIAask/config"
	"botIAask/internal/guard"
)

// webhookEventTypes maps X-GitHub-Event names to the Events API type names ExtractAnnouncement
// understands. The payloads match (push aside, see extractPush), so one code path serves both.
var webhookEventTypes = map[string]string{
	"push": "PushEvent", "pull_request": "PullRequestEvent", "release": "ReleaseEvent",
	"issues": "IssuesEvent", "create": "CreateEvent", "delete": "DeleteEvent",
}

type webhookJob struct {
	repo  config.GitHubTrackerRepoConfig
	token string
	ev    RawEvent
}

// HandleWebhook verifies (HMAC-SHA256 over the raw body) and queues one webhook delivery,
// returning the HTTP status to answer with. Announcing happens on a single worker so lines go
// out in arrival order and the paced IRC broadcast never holds the HTTP response (GitHub times
// out after 10s). Unlike polling there is no retry here: a delivery refused with 5xx shows as
// failed in the repo's webhook settings and can be redelivered from there.
func (f *Fetcher) HandleWebhook(event, delivery, signature string, body []byte) int {
	f.mu.Lock()
	cfg := f.cfg.GitHubTracker
	f.mu.Unlock()
	if !cfg.Enabled || cfg.WebhookSecret == "" {
		return http.StatusServiceUnavailable
	}
	mac := hmac.New(sha256.New, []byte(cfg.WebhookSecret))
	mac.Write(body)
	if !hmac.Equal([]byte("sha256="+hex.EncodeToString(mac.Sum(nil))), []byte(signature)) {
		return http.StatusUnauthorized
	}
	if event == "ping" {
		return http.StatusOK
	}
	typ, ok := webhookEventTypes[event]
	if !ok {
		return http.StatusNoContent
	}
	var p struct {
		Ref        string `json:"ref"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
	}
	if delivery == "" || json.Unmarshal(body, &p) != nil {
		return http.StatusBadRequest
	}
	owner, name, _ := strings.Cut(p.Repository.FullName, "/")
	repo, _, found := config.FindGitHubTrackerRepo(cfg.Repos, owner, name)
	if !found || !repo.Webhook {
		return http.StatusNotFound
	}
	// A pushed tag or deleted branch also arrives as its own create/delete event.
	if typ == "PushEvent" && (p.Deleted || strings.HasPrefix(p.Ref, "refs/tags/")) {
		return http.StatusNoContent
	}
	// Refuse while IRC is down: nothing re-polls a webhook, so a 5xx lets GitHub redeliver.
	if !f.bot.IsConnected() {
		return http.StatusServiceUnavailable
	}
	token, err := f.cryptor.Decrypt(repo.TokenEncrypted)
	if err != nil {
		token = "" // only used to look up a missing commit title; the announcement still goes out
	}
	ev := RawEvent{ID: delivery, Type: typ, Payload: body, CreatedAt: time.Now()}
	ev.Actor.Login, ev.Repo.Name = p.Sender.Login, p.Repository.FullName

	f.hookOnce.Do(func() { guard.Go("github webhook", f.hookWorker) })
	select {
	case f.hooks <- webhookJob{repo, token, ev}:
		return http.StatusAccepted
	default: // ponytail: bounded queue, 64 pending deliveries; a flood gets 503 (GitHub redelivers)
		return http.StatusServiceUnavailable
	}
}

func (f *Fetcher) hookWorker() {
	for j := range f.hooks {
		f.announceNewEvents(j.repo, j.token, -1, []RawEvent{j.ev})
	}
}
