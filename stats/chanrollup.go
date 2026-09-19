package stats

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"botIAask/logger"
)

// Per-channel daily rollups (web "Channel Stats"). One row per network+channel+day holds
// 24 hourly counts plus small top-N JSON maps (~0.5 KB/row), built from the IRC logs so
// history outlives log rotation without keeping the raw logs.

const (
	topNicks = 25
	topCmds  = 30
	topOpers = 5

	// DefaultChanRetentionDays applies when stats.chan_retention_days is unset.
	DefaultChanRetentionDays = 365
)

func createChanActivity(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chan_activity (
			network TEXT NOT NULL,
			channel TEXT NOT NULL,
			day TEXT NOT NULL,
			msgs INTEGER NOT NULL DEFAULT 0,
			actions INTEGER NOT NULL DEFAULT 0,
			joins INTEGER NOT NULL DEFAULT 0,
			parts INTEGER NOT NULL DEFAULT 0,
			topics INTEGER NOT NULL DEFAULT 0,
			hours TEXT NOT NULL,
			nicks TEXT NOT NULL DEFAULT '{}',
			cmds TEXT NOT NULL DEFAULT '{}',
			modes TEXT NOT NULL DEFAULT '{}',
			mode_by TEXT NOT NULL DEFAULT '{}',
			topic_log TEXT NOT NULL DEFAULT '[]',
			PRIMARY KEY (network, channel, day)
		) WITHOUT ROWID`)
	if err != nil {
		return fmt.Errorf("failed to create chan_activity table: %w", err)
	}
	return nil
}

// NetNick names a configured network and the bot's nick on it (its own lines are skipped).
type NetNick struct{ Name, Nick string }

func topN(m map[string]int, n int) map[string]int {
	if len(m) <= n {
		return m
	}
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(m))
	for k, v := range m {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	out := make(map[string]int, n)
	for _, e := range all[:n] {
		out[e.k] = e.v
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// UpsertChanDay stores (or replaces) one channel-day.
func (d *Database) UpsertChanDay(network, channel, day string, r logger.DayRollup) error {
	_, err := d.db.Exec(`INSERT OR REPLACE INTO chan_activity
		(network, channel, day, msgs, actions, joins, parts, topics, hours, nicks, cmds, modes, mode_by, topic_log)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		network, channel, day, r.Msgs, r.Actions, r.Joins, r.Parts, r.Topics,
		mustJSON(r.Hours), mustJSON(topN(r.Nicks, topNicks)), mustJSON(topN(r.Cmds, topCmds)),
		mustJSON(r.Modes), mustJSON(topN(r.ModeBy, topOpers)), mustJSON(r.TopicLog))
	return err
}

// splitLogKey maps a log file key ("<network>_<chan without first #>") back to network+channel.
// Longest network-name prefix wins; PM logs (key == network) and unknown networks return ok=false.
func splitLogKey(key string, nets []NetNick) (network, channel string, ok bool) {
	for _, n := range nets {
		if strings.HasPrefix(key, n.Name+"_") && len(n.Name) > len(network) {
			network, channel = n.Name, "#"+key[len(n.Name)+1:]
		}
	}
	return network, strings.ToLower(channel), network != ""
}

func parseLogFileName(name string) (key, day string, gz bool, ok bool) {
	base := name
	if strings.HasSuffix(base, ".log.gz") {
		base, gz = strings.TrimSuffix(base, ".gz"), true
	}
	if !strings.HasSuffix(base, ".log") {
		return "", "", false, false
	}
	base = strings.TrimSuffix(base, ".log")
	if len(base) < 12 || base[len(base)-11] != '_' {
		return "", "", false, false
	}
	return base[:len(base)-11], base[len(base)-10:], gz, true
}

// RollupLogs parses log files under dirs into chan_activity. Days already stored are skipped
// unless they're within the last recentDays (today is still being written), so the first run
// backfills everything on disk and later runs are cheap. Returns rows written.
func (d *Database) RollupLogs(dirs []string, nets []NetNick, prefix string, recentDays int) int {
	have := map[string]bool{}
	if rows, err := d.db.Query(`SELECT network, channel, day FROM chan_activity`); err == nil {
		for rows.Next() {
			var n, c, day string
			if rows.Scan(&n, &c, &day) == nil {
				have[n+"\x00"+c+"\x00"+day] = true
			}
		}
		rows.Close()
	}
	cutoff := time.Now().AddDate(0, 0, -(recentDays - 1)).Format("2006-01-02")
	nickOf := map[string]string{}
	for _, n := range nets {
		nickOf[n.Name] = n.Nick
	}
	written := 0
	done := map[string]bool{} // plain log wins over an archived copy of the same day
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			key, day, gz, ok := parseLogFileName(e.Name())
			if !ok {
				continue
			}
			network, channel, ok := splitLogKey(key, nets)
			if !ok {
				continue
			}
			id := network + "\x00" + channel + "\x00" + day
			if done[id] || (have[id] && day < cutoff) {
				continue
			}
			f, err := os.Open(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			var r io.Reader = f
			var zr *gzip.Reader
			if gz {
				if zr, err = gzip.NewReader(f); err != nil {
					f.Close()
					continue
				}
				r = zr
			}
			roll := logger.ParseDayLog(r, nickOf[network], prefix)
			if zr != nil {
				zr.Close()
			}
			f.Close()
			if err := d.UpsertChanDay(network, channel, day, roll); err != nil {
				log.Printf("chan rollup %s %s %s: %v", network, channel, day, err)
				continue
			}
			done[id] = true
			written++
		}
	}
	return written
}

// PruneChanActivity drops rollup rows older than keepDays.
func (d *Database) PruneChanActivity(keepDays int) {
	if keepDays <= 0 {
		keepDays = DefaultChanRetentionDays
	}
	if _, err := d.db.Exec(`DELETE FROM chan_activity WHERE day < ?`, time.Now().AddDate(0, 0, -keepDays).Format("2006-01-02")); err != nil {
		log.Printf("chan_activity prune: %v", err)
	}
}
