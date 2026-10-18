package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"botIAask/config"
	"botIAask/irc"
)

func ircWebChannelNameOK(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) > 0 && (s[0] == '#' || s[0] == '&')
}

type ircChannelRow struct {
	Name        string `json:"name"`
	HasPassword bool   `json:"has_password"`
	AnnounceRSS bool   `json:"announce_rss"`
	AutoJoin    bool   `json:"auto_join"`
}

type ircSessionRow struct {
	Name        string `json:"name"`
	HasPassword bool   `json:"has_password"`
}

type ircNetworkRow struct {
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	Server        string `json:"server"`
	Port          int    `json:"port"`
	UseSSL        bool   `json:"use_ssl"`
	TLSSkipVerify bool   `json:"tls_skip_verify"`
	BotMode       bool   `json:"bot_mode"`
	Nickname      string `json:"nickname"`
	QuitMessage   string `json:"quit_message"`
	SASLEnabled   bool   `json:"sasl_enabled"`
	SASLUsername  string `json:"sasl_username"`
	SASLMechanism string `json:"sasl_mechanism"`
	HasNickServ   bool   `json:"has_nickserv_password"`
	CertFP        string `json:"cert_fingerprint,omitempty"`
	AcctReg       bool   `json:"account_registration"`
	ChannelCount  int    `json:"channel_count"`
	Connected     bool   `json:"connected"`
	Authenticated bool   `json:"authenticated"`
}

// handleConfigIRCAdmins manages admin hostmasks. An empty/absent "network" scope
// (query param on GET/DELETE, JSON field on POST) targets the global admin.admins
// list (bot-wide, every network); a non-empty scope targets that one network's own
// admins list (config.IRCNetworkConfig.Admins, that network only). Unlike
// resolveWebNetwork (channels etc.), empty here means "global", not "first network".
func (s *Server) handleConfigIRCAdmins(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		network := strings.TrimSpace(r.URL.Query().Get("network"))
		cfg := s.getConfig()
		var list []string
		if network == "" {
			list = append([]string(nil), cfg.Admin.Admins...)
		} else {
			netCfg, ok := config.FindIRCNetworkByName(cfg.IRC.Networks, network)
			if !ok {
				http.Error(w, "Unknown network", http.StatusNotFound)
				return
			}
			list = append([]string(nil), netCfg.Admins...)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"hostmasks": list, "network": network})

	case http.MethodPost:
		var req struct {
			Hostmask string `json:"hostmask"`
			Network  string `json:"network"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		h := strings.TrimSpace(req.Hostmask)
		if h == "" {
			http.Error(w, "hostmask required", http.StatusBadRequest)
			return
		}
		network := strings.TrimSpace(req.Network)
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			list, ok := adminList(cfg, network)
			if !ok {
				return http.StatusNotFound, errors.New("Unknown network")
			}
			if slices.Contains(*list, h) {
				return http.StatusConflict, errors.New("already in list")
			}
			*list = append(*list, h)
			return 0, nil
		}) {
			return
		}
		if err := s.runFullRehashFromWeb("web (config irc-admins)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})

	case http.MethodDelete:
		raw := strings.TrimSpace(r.URL.Query().Get("hostmask"))
		if raw == "" {
			http.Error(w, "hostmask required", http.StatusBadRequest)
			return
		}
		if dec, err := url.QueryUnescape(raw); err == nil && dec != "" {
			raw = strings.TrimSpace(dec)
		}
		network := strings.TrimSpace(r.URL.Query().Get("network"))
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			list, ok := adminList(cfg, network)
			if !ok {
				return http.StatusNotFound, errors.New("Unknown network")
			}
			i := slices.Index(*list, raw)
			if i < 0 {
				return http.StatusNotFound, errors.New("not found")
			}
			*list = slices.Delete(*list, i, i+1)
			return 0, nil
		}) {
			return
		}
		if err := s.runFullRehashFromWeb("web (config irc-admins)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

var errUnknownNetwork = errors.New("Unknown network")

// resolveWebNetwork returns name trimmed, or the first configured network's name if
// name is empty (back-compat default for single-network callers/UI).
func (s *Server) resolveWebNetwork(name string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	if nets := s.getConfig().IRC.Networks; len(nets) > 0 {
		return nets[0].Name
	}
	return ""
}

func (s *Server) handleIRCChannels(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := s.getConfig()
		network := s.resolveWebNetwork(r.URL.Query().Get("network"))
		netCfg, ok := config.FindIRCNetworkByName(cfg.IRC.Networks, network)
		if !ok {
			http.Error(w, "Unknown network", http.StatusNotFound)
			return
		}
		defaultNet := config.DefaultRSSNetwork(cfg.IRC.Networks)
		rows := make([]ircChannelRow, 0, len(netCfg.Channels))
		for _, ch := range netCfg.Channels {
			rows = append(rows, ircChannelRow{
				Name:        ch.Name,
				HasPassword: ch.Password != "",
				AnnounceRSS: config.RSSChannelContainsFold(cfg.RSS.Channels, network, ch.Name, defaultNet),
				AutoJoin:    ch.AutoJoinEnabled(),
			})
		}
		sessRows := make([]ircSessionRow, 0)
		if s.bot != nil {
			sess := s.bot.ListSessionChannels(network)
			for _, ch := range sess {
				sessRows = append(sessRows, ircSessionRow{Name: ch.Name, HasPassword: ch.Password != ""})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"network": network, "channels": rows, "session_channels": sessRows})
		return

	case http.MethodPost:
		var req struct {
			Network     string `json:"network"`
			Name        string `json:"name"`
			Password    string `json:"password"`
			AutoJoin    *bool  `json:"auto_join"`
			SessionOnly bool   `json:"session_only"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		network := s.resolveWebNetwork(req.Network)
		name := strings.TrimSpace(req.Name)
		if !ircWebChannelNameOK(name) {
			http.Error(w, "Invalid channel name (use #chan or &chan)", http.StatusBadRequest)
			return
		}
		if req.SessionOnly {
			if s.bot == nil {
				http.Error(w, "Bot unavailable", http.StatusServiceUnavailable)
				return
			}
			if err := s.bot.JoinChannelSession(network, config.IRChannel{Name: name, Password: req.Password}); err != nil {
				if strings.Contains(err.Error(), "already") {
					http.Error(w, err.Error(), http.StatusConflict)
					return
				}
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true, "session": true})
			return
		}
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			ni := config.IRCNetworkIndex(cfg.IRC.Networks, network)
			if ni < 0 {
				return http.StatusNotFound, errUnknownNetwork
			}
			if _, exists := config.FindIRChannelByName(cfg.IRC.Networks[ni].Channels, name); exists {
				return http.StatusConflict, errors.New("Channel already in autoinjoin list")
			}
			cfg.IRC.Networks[ni].Channels = append(cfg.IRC.Networks[ni].Channels,
				config.IRChannel{Name: name, Password: req.Password, AutoJoin: req.AutoJoin})
			return 0, nil
		}) {
			return
		}
		if err := s.runFullRehashFromWeb("web (irc channels add)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return

	case http.MethodDelete:
		network := s.resolveWebNetwork(r.URL.Query().Get("network"))
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if d, err := url.QueryUnescape(name); err == nil && d != "" {
			name = strings.TrimSpace(d)
		}
		if !ircWebChannelNameOK(name) {
			http.Error(w, "Invalid or missing name query", http.StatusBadRequest)
			return
		}
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			ni := config.IRCNetworkIndex(cfg.IRC.Networks, network)
			if ni < 0 {
				return http.StatusNotFound, errUnknownNetwork
			}
			canon, ok := config.FindIRChannelByName(cfg.IRC.Networks[ni].Channels, name)
			if !ok {
				return http.StatusNotFound, errors.New("Channel not found")
			}
			cfg.IRC.Networks[ni].Channels = slices.DeleteFunc(slices.Clone(cfg.IRC.Networks[ni].Channels), func(ch config.IRChannel) bool {
				return strings.EqualFold(ch.Name, name)
			})
			cfg.RSS.Channels = config.SetRSSChannelAnnounce(cfg.RSS.Channels, network, canon.Name, false, config.DefaultRSSNetwork(cfg.IRC.Networks))
			return 0, nil
		}) {
			return
		}
		if err := s.runFullRehashFromWeb("web (irc channels remove)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleIRCChannelReveal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		Network string `json:"network"`
		Name    string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	network := s.resolveWebNetwork(req.Network)
	name := strings.TrimSpace(req.Name)
	if !ircWebChannelNameOK(name) {
		http.Error(w, "Invalid channel name", http.StatusBadRequest)
		return
	}
	netCfg, ok := config.FindIRCNetworkByName(s.getConfig().IRC.Networks, network)
	if !ok {
		http.Error(w, "Unknown network", http.StatusNotFound)
		return
	}
	if ch, ok := config.FindIRChannelByName(netCfg.Channels, name); ok {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"password": ch.Password})
		return
	}
	if s.bot != nil {
		for _, ch := range s.bot.ListSessionChannels(network) {
			if strings.EqualFold(ch.Name, name) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{"password": ch.Password})
				return
			}
		}
	}
	http.Error(w, "Channel not found", http.StatusNotFound)
}

func (s *Server) handleIRCChannelAnnounce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		Network  string `json:"network"`
		Name     string `json:"name"`
		Announce *bool  `json:"announce"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Announce == nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	network := s.resolveWebNetwork(req.Network)
	name := strings.TrimSpace(req.Name)
	if !ircWebChannelNameOK(name) {
		http.Error(w, "Invalid channel name", http.StatusBadRequest)
		return
	}
	if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
		netCfg, ok := config.FindIRCNetworkByName(cfg.IRC.Networks, network)
		if !ok {
			return http.StatusNotFound, errUnknownNetwork
		}
		entry, ok := config.FindIRChannelByName(netCfg.Channels, name)
		if !ok {
			return http.StatusNotFound, errors.New("Channel not in autoinjoin list")
		}
		cfg.RSS.Channels = config.SetRSSChannelAnnounce(cfg.RSS.Channels, network, entry.Name, *req.Announce, config.DefaultRSSNetwork(cfg.IRC.Networks))
		return 0, nil
	}) {
		return
	}
	if s.rssFetcher != nil {
		s.rssFetcher.ApplyConfig(s.getConfig())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleIRCChannelAutojoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		Network  string `json:"network"`
		Name     string `json:"name"`
		AutoJoin *bool  `json:"auto_join"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AutoJoin == nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	network := s.resolveWebNetwork(req.Network)
	name := strings.TrimSpace(req.Name)
	if !ircWebChannelNameOK(name) {
		http.Error(w, "Invalid channel name", http.StatusBadRequest)
		return
	}
	if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
		ni := config.IRCNetworkIndex(cfg.IRC.Networks, network)
		if ni < 0 {
			return http.StatusNotFound, errUnknownNetwork
		}
		for i, ex := range cfg.IRC.Networks[ni].Channels {
			if strings.EqualFold(ex.Name, name) {
				cfg.IRC.Networks[ni].Channels[i].AutoJoin = req.AutoJoin
				return 0, nil
			}
		}
		return http.StatusNotFound, errors.New("Channel not found")
	}) {
		return
	}
	if err := s.runFullRehashFromWeb("web (irc autoin)"); err != nil {
		http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleIRCChannelSession(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.bot == nil {
		http.Error(w, "Bot unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Network  string `json:"network"`
			Name     string `json:"name"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		network := s.resolveWebNetwork(req.Network)
		name := strings.TrimSpace(req.Name)
		if !ircWebChannelNameOK(name) {
			http.Error(w, "Invalid channel name (use #chan or &chan)", http.StatusBadRequest)
			return
		}
		if err := s.bot.JoinChannelSession(network, config.IRChannel{Name: name, Password: req.Password}); err != nil {
			if strings.Contains(err.Error(), "already") {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	case http.MethodDelete:
		network := s.resolveWebNetwork(r.URL.Query().Get("network"))
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if d, err := url.QueryUnescape(name); err == nil && d != "" {
			name = strings.TrimSpace(d)
		}
		if !ircWebChannelNameOK(name) {
			http.Error(w, "Invalid or missing name query", http.StatusBadRequest)
			return
		}
		if err := s.bot.PartChannelSession(network, name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleIRCNetworks: GET lists configured networks (joined with live status), POST adds
// one, DELETE removes one by ?name=. Mirrors handleIRCChannels' persist-then-rehash flow.
func (s *Server) handleIRCNetworks(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := s.getConfig()
		statuses := map[string]irc.NetworkStatus{}
		if s.bot != nil {
			for _, st := range s.bot.NetworkStatuses() {
				statuses[strings.ToLower(st.Name)] = st
			}
		}
		rows := make([]ircNetworkRow, 0, len(cfg.IRC.Networks))
		for _, n := range cfg.IRC.Networks {
			row := ircNetworkRow{
				Name:          n.Name,
				Enabled:       n.IsEnabled(),
				Server:        n.Server,
				Port:          n.Port,
				UseSSL:        n.UseSSL,
				TLSSkipVerify: n.TLSSkipVerify,
				BotMode:       n.BotMode,
				Nickname:      n.Nickname,
				QuitMessage:   n.QuitMessage,
				SASLEnabled:   n.Services.Enabled,
				SASLUsername:  n.Services.Username,
				SASLMechanism: strings.ToLower(n.Services.Mechanism),
				HasNickServ:   n.Services.NickServPassword != "",
				ChannelCount:  len(n.Channels),
			}
			if n.Services.ClientCert != "" {
				row.CertFP, _ = config.ClientCertFingerprint(n.Services.ClientCert)
			}
			if st, ok := statuses[strings.ToLower(n.Name)]; ok {
				row.Connected = st.Connected
				row.Authenticated = st.Authenticated
				row.AcctReg = st.AccountRegistration
			}
			rows = append(rows, row)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"networks": rows})
		return

	case http.MethodPost:
		var req struct {
			Name          string   `json:"name"`
			Enabled       *bool    `json:"enabled"`
			Server        string   `json:"server"`
			Port          int      `json:"port"`
			UseSSL        bool     `json:"use_ssl"`
			TLSSkipVerify bool     `json:"tls_skip_verify"`
			BotMode       bool     `json:"bot_mode"`
			Nickname      string   `json:"nickname"`
			QuitMessage   string   `json:"quit_message"`
			Admins        []string `json:"admins"`
			Channels      []struct {
				Name     string `json:"name"`
				Password string `json:"password"`
				AutoJoin *bool  `json:"auto_join"`
			} `json:"channels"`
			SASL *struct {
				Enabled   bool   `json:"enabled"`
				Username  string `json:"username"`
				Password  string `json:"password"`
				Mechanism string `json:"mechanism"`
			} `json:"sasl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(req.Name)
		server := strings.TrimSpace(req.Server)
		nickname := strings.TrimSpace(req.Nickname)
		if name == "" || strings.Contains(name, ":") {
			http.Error(w, "Invalid network name (required, no ':')", http.StatusBadRequest)
			return
		}
		if server == "" || req.Port <= 0 || req.Port > 65535 || nickname == "" {
			http.Error(w, "server, port and nickname are required", http.StatusBadRequest)
			return
		}
		entry := config.IRCNetworkConfig{
			Name: name, Enabled: req.Enabled, Server: server, Port: req.Port, UseSSL: req.UseSSL,
			TLSSkipVerify: req.TLSSkipVerify, BotMode: req.BotMode, Nickname: nickname, QuitMessage: strings.TrimSpace(req.QuitMessage),
			Admins: req.Admins,
		}
		for _, ch := range req.Channels {
			chName := strings.TrimSpace(ch.Name)
			if chName == "" {
				continue
			}
			entry.Channels = append(entry.Channels, config.IRChannel{Name: chName, Password: ch.Password, AutoJoin: ch.AutoJoin})
		}
		if req.SASL != nil {
			entry.Services = config.ServicesConfig{Enabled: req.SASL.Enabled, Username: req.SASL.Username, Password: req.SASL.Password, Mechanism: strings.ToLower(strings.TrimSpace(req.SASL.Mechanism))}
		}
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			if _, exists := config.FindIRCNetworkByName(cfg.IRC.Networks, name); exists {
				return http.StatusConflict, errors.New("Network already exists")
			}
			newNets := append(slices.Clone(cfg.IRC.Networks), entry)
			if err := config.ValidateConfig(&config.Config{IRC: config.IRCConfig{Networks: newNets}}); err != nil {
				return http.StatusBadRequest, err
			}
			cfg.IRC.Networks = newNets
			return 0, nil
		}) {
			return
		}
		if err := s.runFullRehashFromWeb("web (irc networks add)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return

	case http.MethodDelete:
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if d, err := url.QueryUnescape(name); err == nil && d != "" {
			name = strings.TrimSpace(d)
		}
		if name == "" {
			http.Error(w, "Missing name query", http.StatusBadRequest)
			return
		}
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			if _, ok := config.FindIRCNetworkByName(cfg.IRC.Networks, name); !ok {
				return http.StatusNotFound, errors.New("Network not found")
			}
			out := slices.DeleteFunc(slices.Clone(cfg.IRC.Networks), func(n config.IRCNetworkConfig) bool {
				return strings.EqualFold(n.Name, name)
			})
			if err := config.ValidateConfig(&config.Config{IRC: config.IRCConfig{Networks: out}}); err != nil {
				return http.StatusBadRequest, errors.New("Cannot remove: " + err.Error())
			}
			cfg.IRC.Networks = out
			return 0, nil
		}) {
			return
		}
		if err := s.runFullRehashFromWeb("web (irc networks remove)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleIRCNetworkEdit updates an existing network's connection fields (server/port/
// ssl/nickname/quit_message/sasl); the name itself is immutable here (rename = remove+add).
func (s *Server) handleIRCNetworkEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
		Server  string `json:"server"`
		Port    int    `json:"port"`
		UseSSL  bool   `json:"use_ssl"`
		// TLSSkipVerify and QuitMessage are pointers, like Enabled and SASL below: a nil
		// value means "leave as configured", so a client (the dashboard form, or any older
		// cached JS/script) that omits a field it doesn't know about can't silently wipe it
		// — this is the fix for a real bug where the dashboard's edit form used to erase
		// quit_message on every save because it never sent that field at all.
		TLSSkipVerify *bool   `json:"tls_skip_verify"`
		BotMode       *bool   `json:"bot_mode"`
		Nickname      string  `json:"nickname"`
		QuitMessage   *string `json:"quit_message"`
		SASL          *struct {
			Enabled   bool   `json:"enabled"`
			Username  string `json:"username"`
			Password  string `json:"password"`
			Mechanism string `json:"mechanism"`
		} `json:"sasl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	server := strings.TrimSpace(req.Server)
	nickname := strings.TrimSpace(req.Nickname)
	if name == "" || server == "" || req.Port <= 0 || req.Port > 65535 || nickname == "" {
		http.Error(w, "name, server, port and nickname are required", http.StatusBadRequest)
		return
	}
	if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
		ni := config.IRCNetworkIndex(cfg.IRC.Networks, name)
		if ni < 0 {
			return http.StatusNotFound, errors.New("Network not found")
		}
		n := &cfg.IRC.Networks[ni]
		if req.Enabled != nil {
			n.Enabled = req.Enabled
		}
		n.Server, n.Port, n.UseSSL = server, req.Port, req.UseSSL
		if req.TLSSkipVerify != nil {
			n.TLSSkipVerify = *req.TLSSkipVerify
		}
		if req.BotMode != nil {
			n.BotMode = *req.BotMode
		}
		n.Nickname = nickname
		if req.QuitMessage != nil {
			n.QuitMessage = strings.TrimSpace(*req.QuitMessage)
		}
		if req.SASL != nil {
			// The dashboard never reads the stored password back (write-only, like channel
			// keys), so an edit that leaves the password field blank means "keep the one
			// already configured" rather than clearing it.
			password := req.SASL.Password
			if password == "" {
				password = n.Services.Password
			}
			// Keep the NickServ password and client cert: they're managed by their own endpoints.
			svc := n.Services
			svc.Enabled, svc.Username, svc.Password = req.SASL.Enabled, req.SASL.Username, password
			svc.Mechanism = strings.ToLower(strings.TrimSpace(req.SASL.Mechanism))
			n.Services = svc
		}
		if err := config.ValidateConfig(cfg); err != nil {
			return http.StatusBadRequest, err
		}
		return 0, nil
	}) {
		return
	}
	if err := s.runFullRehashFromWeb("web (irc networks edit)"); err != nil {
		http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleIRCAccess manages a network's channel access list (auto-op / auto-voice / auto-kick,
// config.IRCNetworkConfig.Access). GET ?network=; POST {network, channel, mask, level, reason}
// adds or replaces the level of channel+mask; DELETE ?network=&channel=&mask=. Auto-kick masks
// are also banned / unbanned right away on the live network.
func (s *Server) handleIRCAccess(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	network := strings.TrimSpace(r.URL.Query().Get("network"))
	switch r.Method {
	case http.MethodGet:
		netCfg, ok := config.FindIRCNetworkByName(s.getConfig().IRC.Networks, network)
		if !ok {
			http.Error(w, "Unknown network", http.StatusNotFound)
			return
		}
		entries := netCfg.Access
		if entries == nil {
			entries = []config.ChannelAccess{}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"network": netCfg.Name, "entries": entries})
		return

	case http.MethodPost:
		var req struct {
			Network string `json:"network"`
			config.ChannelAccess
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		e := req.ChannelAccess
		if err := config.ValidateAccess(&e); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		network = strings.TrimSpace(req.Network)
		var prev string
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			i := config.IRCNetworkIndex(cfg.IRC.Networks, network)
			if i < 0 {
				return http.StatusNotFound, errors.New("Unknown network")
			}
			list := &cfg.IRC.Networks[i].Access
			if j := config.AccessIndex(*list, e.Channel, e.Mask); j >= 0 {
				prev = (*list)[j].Level
				(*list)[j] = e
			} else {
				*list = append(*list, e)
			}
			return 0, nil
		}) {
			return
		}
		if err := s.runFullRehashFromWeb("web (irc access)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if s.bot != nil && (e.Level == config.AccessKick) != (prev == config.AccessKick) {
			s.bot.EnforceAccessBan(network, e.Channel, e.Mask, e.Level == config.AccessKick)
		}

	case http.MethodDelete:
		channel := strings.TrimSpace(r.URL.Query().Get("channel"))
		mask := strings.TrimSpace(r.URL.Query().Get("mask"))
		var removed config.ChannelAccess
		if !s.mutateConfig(w, func(cfg *config.Config) (int, error) {
			i := config.IRCNetworkIndex(cfg.IRC.Networks, network)
			if i < 0 {
				return http.StatusNotFound, errors.New("Unknown network")
			}
			list := &cfg.IRC.Networks[i].Access
			j := config.AccessIndex(*list, channel, mask)
			if j < 0 {
				return http.StatusNotFound, errors.New("not found")
			}
			removed = (*list)[j]
			*list = slices.Delete(*list, j, j+1)
			return 0, nil
		}) {
			return
		}
		if err := s.runFullRehashFromWeb("web (irc access)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if s.bot != nil && removed.Level == config.AccessKick {
			s.bot.EnforceAccessBan(network, removed.Channel, removed.Mask, false)
		}

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
