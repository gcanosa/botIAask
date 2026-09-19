package stats

import (
	"time"

	"botIAask/config"
)

// ChanReport returns the Channel Stats aggregate (empty when the DB is unavailable).
func (t *Tracker) ChanReport(days int, network, channel string) (ChanReport, error) {
	if t.db == nil {
		return ChanReport{Days: days, Modes: map[string]int{}}, nil
	}
	return t.db.ChanReport(days, network, channel)
}

// RunChanRollup keeps chan_activity current: backfills from logs/ and logs/archive/ on start,
// then refreshes the last two days every 10 minutes and prunes by chan_retention_days.
// Blocks; run it in its own goroutine. getCfg is called each cycle so rehash is honored.
func RunChanRollup(db *Database, getCfg func() *config.Config) {
	if db == nil {
		return
	}
	run := func() {
		cfg := getCfg()
		nets := make([]NetNick, 0, len(cfg.IRC.Networks))
		for _, n := range cfg.IRC.Networks {
			nets = append(nets, NetNick{n.Name, n.Nickname})
		}
		db.RollupLogs([]string{"logs", "logs/archive"}, nets, cfg.Bot.CommandPrefix, 2)
		db.PruneChanActivity(cfg.Stats.ChanRetentionDays)
	}
	run()
	for range time.Tick(10 * time.Minute) {
		run()
	}
}
