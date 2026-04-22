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
	MinDate       string `json:"min_date"`
	MaxDate       string `json:"max_date"`
	RotationDays  int    `json:"rotation_days"`
	LocalToday    string `json:"server_local_today"`
}

type logChannelEntry struct {
	Label         string   `json:"label"`
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
	rotationDays := s.cfg.Logger.RotationDays

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

	joinedByKey := make(map[string]string)
	for _, ch := range s.cfg.IRC.Channels {
		k := logger.ChannelFileKey(ch.Name, s.cfg.IRC.Server)
		joinedByKey[k] = ch.Name
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
		label, joined := joinedByKey[fileKey]
		if !joined {
			label = "#" + fileKey
		}
		dateSet := diskDates[fileKey]
		dates := make([]string, 0, len(dateSet))
		for d := range dateSet {
			dates = append(dates, d)
		}
		sort.Strings(dates)
		channels = append(channels, logChannelEntry{
			Label:         label,
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
	Lines   []string `json:"lines"`
	Truncated bool   `json:"truncated"`
	Date    string   `json:"date"`
	Archived bool    `json:"archived"`
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

	if _, err := time.ParseInLocation("2006-01-02", date, time.Local); err != nil {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}

	key := logger.ChannelFileKey(channel, s.cfg.IRC.Server)
	activePath := filepath.Join(logsDir, fmt.Sprintf("%s_%s.log", key, date))
	archivePath := filepath.Join(logsArchiveDir, fmt.Sprintf("%s_%s.log.gz", key, date))

	var reader io.ReadCloser
	archived := false
	if f, err := os.Open(activePath); err == nil {
		reader = f
	} else if f, err := os.Open(archivePath); err == nil {
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			http.Error(w, "bad archive", http.StatusInternalServerError)
			return
		}
		reader = &readCloserPair{rc: gz, closeUnderlying: f}
		archived = true
	} else {
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
