package web

import (
	"bufio"
	"bytes"
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

	"botIAask/config"
	"botIAask/logger"
)

const (
	maxHistoryLines = 40000
	logsDir         = "logs"
	logsArchiveDir  = "logs/archive"
	debugLogKey     = "bot" // logs/debug/bot_<date>.log, see logger.StartDebugLog
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
	Debug         bool     `json:"debug,omitempty"` // the bot's process log (admins only), not a channel
}

type logCatalogResponse struct {
	Calendar logCalendarMeta   `json:"calendar"`
	Channels []logChannelEntry `json:"channels"`
}

// validLogTarget guards the public log endpoints. The channel must be a real channel name:
// anything else maps (via logger.ChannelFileKey) to the bot's private-message log, which is
// not meant to be public. The network, if given, must be a configured one so it can't be used
// to walk out of logs/ ("../").
func validLogTarget(cfg *config.Config, channel, network string) bool {
	if channel == "" || (channel[0] != '#' && channel[0] != '&') {
		return false
	}
	if network == "" {
		return true
	}
	for _, n := range cfg.IRC.Networks {
		if n.Name == network {
			return true
		}
	}
	return false
}

// isNetworkLogKey reports whether a bare log key is a configured network name: that file is
// the bot's private-message log (logs/<network>_<date>.log), never a channel.
func isNetworkLogKey(cfg *config.Config, key string) bool {
	for _, n := range cfg.IRC.Networks {
		if strings.EqualFold(n.Name, key) {
			return true
		}
	}
	return false
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

	isAdmin := s.staffAdminFromRequest(r)
	// The listing walks logs/ and logs/archive/ (thousands of names after a year) and is public:
	// serve a short-lived copy per audience (admins also see keyed channels and the debug log).
	if body := s.cachedLogCatalog(isAdmin); body != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
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
	// Keys are case-folded: the logger names files after the channel as the server spelled it,
	// so "#News" and "#news" on the same network are one channel split across files.
	diskDates := make(map[string]map[string]struct{})
	addDate := func(key, date string) {
		key = strings.ToLower(key)
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
			newKey := strings.ToLower(logger.ChannelFileKey(ch.Name, net.Name))
			joinedByKey[newKey] = joinedInfo{label: ch.Name, network: net.Name}
			legacyKeyOf[strings.ToLower(logger.ChannelFileKey(ch.Name, ""))] = newKey
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

	// Private-message logs are written under the bare network name (no channel
	// segment) by logger.ChannelFileKey. They aren't channels, so keep them out
	// of the channel browser instead of showing them as bogus "#NetworkName" rows.
	networkNames := make(map[string]struct{})
	for _, net := range cfg.IRC.Networks {
		networkNames[strings.ToLower(net.Name)] = struct{}{}
	}

	keysSorted := make([]string, 0, len(allKeys))
	for k := range allKeys {
		if _, isNetworkName := networkNames[k]; isNetworkName {
			continue
		}
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
				if prefix := strings.ToLower(net.Name) + "_"; strings.HasPrefix(fileKey, prefix) {
					info = joinedInfo{label: "#" + strings.TrimPrefix(fileKey, prefix), network: net.Name}
					break
				}
			}
		}
		if !isAdmin && config.IsKeyedChannel(cfg, info.label, info.network) {
			continue
		}
		dateSet := diskDates[fileKey]
		dates := make([]string, 0, len(dateSet))
		for d := range dateSet {
			// Days before the retention window can't be picked in the calendar anyway.
			if d >= minDate {
				dates = append(dates, d)
			}
		}
		// A channel the bot no longer sits in drops off the list once its last day ages out
		// of retention (the files stay on disk under logs/archive/).
		if !joined && len(dates) == 0 {
			continue
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

	if isAdmin {
		channels = append(channels, debugLogEntry(minDate))
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

	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	s.storeLogCatalog(isAdmin, body)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

const logCatalogTTL = 30 * time.Second

func catalogSlot(isAdmin bool) int {
	if isAdmin {
		return 1
	}
	return 0
}

func (s *Server) cachedLogCatalog(isAdmin bool) []byte {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	i := catalogSlot(isAdmin)
	if s.catalogBody[i] != nil && time.Since(s.catalogAt[i]) < logCatalogTTL {
		return s.catalogBody[i]
	}
	return nil
}

func (s *Server) storeLogCatalog(isAdmin bool, body []byte) {
	s.catalogMu.Lock()
	i := catalogSlot(isAdmin)
	s.catalogBody[i], s.catalogAt[i] = body, time.Now()
	s.catalogMu.Unlock()
}

// debugLogEntry lists the days that have a process log in logs/debug (or its archive).
func debugLogEntry(minDate string) logChannelEntry {
	dir := logger.DebugDir()
	set := make(map[string]struct{})
	for _, d := range []string{dir, filepath.Join(dir, "archive")} {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			name, ok := strings.CutSuffix(e.Name(), ".gz")
			if !ok && d != dir {
				continue
			}
			if key, date, ok := parseLogBaseName(name); ok && key == debugLogKey && date >= minDate {
				set[date] = struct{}{}
			}
		}
	}
	dates := make([]string, 0, len(set))
	for d := range set {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	return logChannelEntry{Label: "Bot debug log", FileKey: debugLogKey, Joined: true, DatesWithLogs: dates, Debug: true}
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

	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if r.URL.Query().Get("debug") == "1" {
		if !s.staffAdminFromRequest(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if _, err := time.ParseInLocation("2006-01-02", date, time.Local); err != nil {
			http.Error(w, "invalid date", http.StatusBadRequest)
			return
		}
		s.writeLogHistory(w, logger.DebugDir(), []string{debugLogKey}, date)
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
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

	if !validLogTarget(s.getConfig(), channel, strings.TrimSpace(r.URL.Query().Get("network"))) {
		http.Error(w, "invalid channel or network", http.StatusBadRequest)
		return
	}
	if config.IsKeyedChannel(s.getConfig(), channel, network) && !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if _, err := time.ParseInLocation("2006-01-02", date, time.Local); err != nil {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}

	// Try the current network-prefixed key first, falling back to the legacy bare-channel
	// key so dates logged before multi-network support stay viewable.
	keys := []string{logger.ChannelFileKey(channel, network)}
	if legacy := logger.ChannelFileKey(channel, ""); !isNetworkLogKey(s.getConfig(), legacy) {
		keys = append(keys, legacy)
	}

	s.writeLogHistory(w, logsDir, keys, date)
}

func (s *Server) writeLogHistory(w http.ResponseWriter, dir string, keys []string, date string) {
	lines, truncated, archived, err := readDayLog(dir, keys, date, maxHistoryLines)
	if err != nil {
		http.Error(w, "bad archive", http.StatusInternalServerError)
		return
	}
	if lines == nil {
		lines = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(logHistoryResponse{
		Lines:     lines,
		Truncated: truncated,
		Date:      date,
		Archived:  archived,
	})
}

// readDayLog returns the last maxLines lines logged on date in dir under any of keys, reading every
// spelling of the file (see logger.DayLogPaths) and interleaving them by timestamp. Plain logs
// are used when present, otherwise the gzipped archive copies (archived reports which).
func readDayLog(dir string, keys []string, date string, maxLines int) (lines []string, truncated, archived bool, err error) {
	var plain, gz []string
	for _, key := range keys {
		plain = append(plain, logger.DayLogPaths(dir, key, date, ".log")...)
		gz = append(gz, logger.DayLogPaths(filepath.Join(dir, "archive"), key, date, ".log.gz")...)
	}
	paths := plain
	if len(paths) == 0 {
		paths, archived = gz, true
	}
	for _, p := range paths {
		if !archived { // plain file: read only its tail
			got, trunc, err := readFileTail(p, maxLines)
			if err != nil {
				continue
			}
			lines = append(lines, got...)
			truncated = truncated || trunc
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, false, false, err
		}
		got, trunc := readLogLinesTail(zr, maxLines)
		zr.Close()
		f.Close()
		lines = append(lines, got...)
		truncated = truncated || trunc
	}
	if len(paths) > 1 {
		// Every line starts with "[HH:MM:SS]"; a stable sort keeps same-second lines in file order.
		sort.SliceStable(lines, func(i, j int) bool { return logLineTime(lines[i]) < logLineTime(lines[j]) })
	}
	if len(lines) > maxLines {
		lines, truncated = lines[len(lines)-maxLines:], true
	}
	return lines, truncated, archived, nil
}

func logLineTime(line string) string {
	if len(line) >= 10 && line[0] == '[' {
		return line[1:9]
	}
	return ""
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

// tailLines returns the last maxLines complete lines of f's first size bytes without reading the
// whole file (big channels log tens of MB a day, and these endpoints are public). partial is any
// unterminated text after the last newline (a line still being written); truncated reports that
// earlier lines were left out.
func tailLines(f *os.File, size int64, maxLines int) (lines []string, partial string, truncated bool, err error) {
	const chunk = 64 << 10
	var chunks [][]byte
	pos, newlines := size, 0
	for pos > 0 && newlines <= maxLines {
		n := int64(chunk)
		if n > pos {
			n = pos
		}
		pos -= n
		b := make([]byte, n)
		if _, err := f.ReadAt(b, pos); err != nil && err != io.EOF {
			return nil, "", false, err
		}
		chunks = append(chunks, b)
		newlines += bytes.Count(b, []byte{'\n'})
	}
	data := make([]byte, 0, size-pos)
	for i := len(chunks) - 1; i >= 0; i-- {
		data = append(data, chunks[i]...)
	}
	if pos > 0 { // we started mid-file: the first line may be cut, drop it
		truncated = true
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			data = nil
		}
	}
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		partial = string(data[i+1:])
		data = data[:i]
		lines = strings.Split(string(data), "\n")
	} else {
		partial = string(data)
	}
	if len(lines) > maxLines {
		lines, truncated = lines[len(lines)-maxLines:], true
	}
	return lines, partial, truncated, nil
}

// readFileTail is tailLines over a whole plain-text log.
func readFileTail(path string, maxLines int) (lines []string, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	lines, partial, truncated, err := tailLines(f, fi.Size(), maxLines)
	if partial != "" {
		lines = append(lines, partial)
	}
	return lines, truncated, err
}

func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	// dir/key name the file tailed: a channel log, or (debug=1, admins only) the bot's process log.
	dir, key := logsDir, ""
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if r.URL.Query().Get("debug") == "1" {
		if !s.staffAdminFromRequest(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		dir, key, channel = logger.DebugDir(), debugLogKey, "debug"
	} else {
		if channel == "" {
			http.Error(w, "Channel is required", http.StatusBadRequest)
			return
		}
		network := strings.TrimSpace(r.URL.Query().Get("network"))
		if !validLogTarget(s.getConfig(), channel, network) {
			http.Error(w, "invalid channel or network", http.StatusBadRequest)
			return
		}
		if network == "" {
			if nets := s.getConfig().IRC.Networks; len(nets) > 0 {
				network = nets[0].Name
			}
		}
		if config.IsKeyedChannel(s.getConfig(), channel, network) && !s.staffAdminFromRequest(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		key = logger.ChannelFileKey(channel, network)
	}

	date := strings.TrimSpace(r.URL.Query().Get("date"))
	localToday := time.Now().Format("2006-01-02")
	if date == "" {
		date = localToday
	}
	if _, err := time.ParseInLocation("2006-01-02", date, time.Local); err != nil {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	if date != localToday {
		http.Error(w, "live stream is only available for server local today; use /api/logs/history for past dates", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// The live tail follows the canonical (lower-cased) file the logger writes to. Other
	// spellings of today's file, written before keys were lower-cased, are replayed once on
	// connect and merged by timestamp so earlier lines from today don't go missing.
	var file *os.File
	var reader *bufio.Reader
	var pending string // a line the logger was still writing when we hit EOF
	openCanon := func() {
		// A missing file just means nothing has been logged for this channel yet today;
		// the poll loop below retries every tick.
		if f, err := os.Open(filepath.Join(dir, fmt.Sprintf("%s_%s.log", key, date))); err == nil {
			file, reader, pending = f, bufio.NewReader(f), ""
		}
	}
	// readLines returns the complete lines available; a trailing partial line is kept in
	// pending and finished on a later tick instead of being dropped.
	readLines := func() ([]string, error) {
		var out []string
		for {
			chunk, err := reader.ReadString('\n')
			pending += chunk
			if err != nil {
				if err == io.EOF {
					return out, nil
				}
				return out, err
			}
			out = append(out, strings.TrimSuffix(pending, "\n"))
			pending = ""
		}
	}

	openCanon()
	var initial []string
	var canonInfo os.FileInfo
	if file != nil {
		// Replay only the tail (a busy channel's day file can be tens of MB) and then follow from
		// that point; an unfinished last line is completed by the live loop below.
		if fi, err := file.Stat(); err == nil {
			canonInfo = fi
			initial, pending, _, _ = tailLines(file, fi.Size(), maxHistoryLines)
			_, _ = file.Seek(fi.Size(), io.SeekStart)
			reader.Reset(file)
		}
	}
	merged := false
	for _, p := range logger.DayLogPaths(dir, key, date, ".log") {
		if fi, err := os.Stat(p); err != nil || (canonInfo != nil && os.SameFile(fi, canonInfo)) {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		more, _ := readLogLinesTail(f, maxHistoryLines)
		f.Close()
		initial = append(initial, more...)
		merged = true
	}
	if merged {
		sort.SliceStable(initial, func(i, j int) bool { return logLineTime(initial[i]) < logLineTime(initial[j]) })
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	defer func() {
		if file != nil {
			file.Close()
		}
	}()

	fmt.Fprintf(w, "data: [CONNECTED %s %s]\n\n", channel, date)
	for _, line := range initial {
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if newToday := time.Now().Format("2006-01-02"); newToday != date {
				date = newToday
				if file != nil {
					file.Close()
					file, reader = nil, nil
				}
				fmt.Fprintf(w, "data: [ROLLOVER %s]\n\n", date)
				flusher.Flush()
			}
			if file == nil {
				if openCanon(); file == nil {
					continue
				}
			}
			lines, err := readLines()
			for _, line := range lines {
				fmt.Fprintf(w, "data: %s\n\n", line)
			}
			if err != nil {
				fmt.Fprintf(w, "data: [ERROR] %v\n\n", err)
			}
			if len(lines) > 0 || err != nil {
				flusher.Flush()
			}
		}
	}
}
