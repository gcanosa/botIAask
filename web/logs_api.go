package web

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"botIAask/logger"
)

const (
	maxHistoryLines = 40000
	logsDir         = "logs"
	logsArchiveDir  = "logs/archive"
)

type logCalendarMeta struct {
	MinDate      string `json:"min_date"`
	MaxDate      string `json:"max_date"`
	RotationDays int    `json:"rotation_days"`
	LocalToday   string `json:"server_local_today"`
}

type logChannelEntry struct {
	Label         string   `json:"label"`   // plain channel name, e.g. "#chan" (pass back as ?channel=)
	Network       string   `json:"network"` // network this entry belongs to (pass back as ?network=)
	FileKey       string   `json:"file_key"`
	Joined        bool     `json:"joined"`
	DatesWithLogs []string `json:"dates_with_logs"`
}

type logCatalogResponse struct {
	Calendar logCalendarMeta   `json:"calendar"`
	Channels []logChannelEntry `json:"channels"`
}

func parseLogBaseName(name string) (channelKey, date string, ok bool) {
	base := strings.TrimSuffix(name, ".log")
	if base == name {
		return "", "", false
	}
	if len(base) < 12 {
		return "", "", false
	}
	date = base[len(base)-10:]
	if len(date) != 10 || date[4] != '-' || date[7] != '-' {
		return "", "", false
	}
	channelKey = base[:len(base)-11]
	if channelKey == "" {
		return "", "", false
	}
	return channelKey, date, true
}

func parseArchiveName(name string) (channelKey, date string, ok bool) {
	if !strings.HasSuffix(name, ".log.gz") {
		return "", "", false
	}
	return parseLogBaseName(strings.TrimSuffix(name, ".gz"))
}

func (s *Server) handleLogCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	now := time.Now()
	localToday := now.Format("2006-01-02")
	maxDate := localToday
	cfg := s.getConfig()
	rotationDays := cfg.Logger.RotationDays

	var minDate string
	if rotationDays > 0 {
		minDate = now.AddDate(0, 0, -rotationDays).Format("2006-01-02")
	}

	// file_key -> set of dates
	diskDates := make(map[string]map[string]struct{})
	addDate := func(key, date string) {
		if diskDates[key] == nil {
			diskDates[key] = make(map[string]struct{})
		}
		diskDates[key][date] = struct{}{}
	}

	entries, err := os.ReadDir(logsDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
				continue
			}
			key, date, ok := parseLogBaseName(e.Name())
			if !ok {
				continue
			}
			addDate(key, date)
		}
	}

	archivePath := logsArchiveDir
	if _, err := os.Stat(archivePath); err == nil {
		aentries, err := os.ReadDir(archivePath)
		if err == nil {
			for _, e := range aentries {
				if e.IsDir() {
					continue
				}
				key, date, ok := parseArchiveName(e.Name())
				if !ok {
					continue
				}
				addDate(key, date)
			}
		}
	}

	if rotationDays <= 0 {
		minDate = maxDate
		for _, dates := range diskDates {
			for d := range dates {
				if d < minDate {
					minDate = d
				}
			}
		}
		if minDate == maxDate && len(diskDates) == 0 {
			minDate = localToday
		}
	}

	// joinedByKey maps the current (network-prefixed) file key to its plain channel name
	// and network. legacyKeyOf maps the pre-multi-network bare-channel key to its
	// new-format key, so dates logged before this change still show up under the
	// channel's current label.
	type joinedInfo struct{ label, network string }
	joinedByKey := make(map[string]joinedInfo)
	legacyKeyOf := make(map[string]string)
	for _, net := range cfg.IRC.Networks {
		for _, ch := range net.Channels {
			newKey := logger.ChannelFileKey(ch.Name, net.Name)
			joinedByKey[newKey] = joinedInfo{label: ch.Name, network: net.Name}
			legacyKeyOf[logger.ChannelFileKey(ch.Name, "")] = newKey
		}
	}
	for legacyKey, newKey := range legacyKeyOf {
		if dates, ok := diskDates[legacyKey]; ok {
			for d := range dates {
				addDate(newKey, d)
			}
			delete(diskDates, legacyKey)
		}
	}

	allKeys := make(map[string]struct{})
	for k := range diskDates {
		allKeys[k] = struct{}{}
	}
	for k := range joinedByKey {
		allKeys[k] = struct{}{}
	}

	keysSorted := make([]string, 0, len(allKeys))
	for k := range allKeys {
		keysSorted = append(keysSorted, k)
	}
	sort.Strings(keysSorted)

	channels := make([]logChannelEntry, 0, len(keysSorted))
	for _, fileKey := range keysSorted {
		info, joined := joinedByKey[fileKey]
		if !joined {
			// Best-effort recovery of (channel, network) for a log file whose config
			// entry no longer exists: match the fileKey's network-name prefix.
			info = joinedInfo{label: "#" + fileKey}
			for _, net := range cfg.IRC.Networks {
				if prefix := net.Name + "_"; strings.HasPrefix(fileKey, prefix) {
					info = joinedInfo{label: "#" + strings.TrimPrefix(fileKey, prefix), network: net.Name}
					break
				}
			}
		}
		dateSet := diskDates[fileKey]
		dates := make([]string, 0, len(dateSet))
		for d := range dateSet {
			dates = append(dates, d)
		}
		sort.Strings(dates)
		channels = append(channels, logChannelEntry{
			Label:         info.label,
			Network:       info.network,
			FileKey:       fileKey,
			Joined:        joined,
			DatesWithLogs: dates,
		})
	}

	resp := logCatalogResponse{
		Calendar: logCalendarMeta{
			MinDate:      minDate,
			MaxDate:      maxDate,
			RotationDays: rotationDays,
			LocalToday:   localToday,
		},
		Channels: channels,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type logHistoryResponse struct {
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
	Date      string   `json:"date"`
	Archived  bool     `json:"archived"`
}

func (s *Server) handleLogHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if channel == "" || date == "" {
		http.Error(w, "channel and date are required", http.StatusBadRequest)
		return
	}
	network := strings.TrimSpace(r.URL.Query().Get("network"))
	if network == "" {
		if nets := s.getConfig().IRC.Networks; len(nets) > 0 {
			network = nets[0].Name
		}
	}

	if _, err := time.ParseInLocation("2006-01-02", date, time.Local); err != nil {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}

	// Try the current network-prefixed key first, falling back to the legacy bare-channel
	// key so dates logged before multi-network support stay viewable.
	keys := []string{logger.ChannelFileKey(channel, network), logger.ChannelFileKey(channel, "")}

	var reader io.ReadCloser
	archived := false
	for _, key := range keys {
		activePath := filepath.Join(logsDir, fmt.Sprintf("%s_%s.log", key, date))
		archivePath := filepath.Join(logsArchiveDir, fmt.Sprintf("%s_%s.log.gz", key, date))
		if f, err := os.Open(activePath); err == nil {
			reader = f
			break
		} else if f, err := os.Open(archivePath); err == nil {
			gz, err := gzip.NewReader(f)
			if err != nil {
				f.Close()
				http.Error(w, "bad archive", http.StatusInternalServerError)
				return
			}
			reader = &readCloserPair{rc: gz, closeUnderlying: f}
			archived = true
			break
		}
	}
	if reader == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(logHistoryResponse{Lines: []string{}, Date: date, Archived: false})
		return
	}
	defer reader.Close()

	lines, truncated := readLogLinesTail(reader, maxHistoryLines)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(logHistoryResponse{
		Lines:     lines,
		Truncated: truncated,
		Date:      date,
		Archived:  archived,
	})
}

type readCloserPair struct {
	rc              io.ReadCloser
	closeUnderlying io.Closer
}

func (p *readCloserPair) Read(b []byte) (int, error) { return p.rc.Read(b) }

func (p *readCloserPair) Close() error {
	_ = p.rc.Close()
	return p.closeUnderlying.Close()
}

func readLogLinesTail(r io.Reader, maxLines int) (lines []string, truncated bool) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	for sc.Scan() {
		if len(lines) >= maxLines {
			truncated = true
			lines = append(lines[1:], sc.Text())
		} else {
			lines = append(lines, sc.Text())
		}
	}
	return lines, truncated
}
