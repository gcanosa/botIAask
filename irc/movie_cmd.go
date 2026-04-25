package irc

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"botIAask/omdb"
)

var movieHTTP = &http.Client{Timeout: 22 * time.Second}

func (b *Bot) handleMovieCommand(target, sender, title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %smovie <title> — e.g. %smovie Inception (OMDb; set omdb.api_key or OMDB_API_KEY)", b.prefix, b.prefix))
		return
	}
	if strings.TrimSpace(b.cfg.OMDB.OMDBAPIKeyOrEnv()) == "" {
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: movie: OMDb not configured (set omdb.api_key or OMDB_API_KEY)", sender)))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := omdb.FetchByTitle(ctx, movieHTTP, b.cfg.OMDB.OMDBAPIKeyOrEnv(), b.cfg.OMDB.BaseURL, title)
	if err != nil {
		b.sendPrivmsgMentionedLines(target, sender, fmt.Sprintf("\x0303,01[MOVIE]\x03 %v", err))
		return
	}
	if res == nil || !res.OK {
		msg := "unavailable"
		if res != nil && res.Error != "" {
			msg = res.Error
		}
		b.sendPrivmsgMentionedLines(target, sender, fmt.Sprintf("\x0303,01[MOVIE]\x03 %s", msg))
		return
	}
	head := res.Title
	if res.Year != "" {
		head += " (" + res.Year + ")"
	}
	rating := res.ImdbRating
	if rating == "" {
		rating = "N/A"
	}
	line := fmt.Sprintf("\x0303,01[MOVIE]\x03 %s · IMDb %s — %s", head, rating, res.Plot)
	b.sendPrivmsgMentionedLines(target, sender, line)
}
