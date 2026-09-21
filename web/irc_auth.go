package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"botIAask/config"
)

// updateNetworkServices applies fn to one network's Services block, then saves the config.
// Secrets are encrypted by config.SaveConfig; nothing here writes them anywhere else.
func (s *Server) updateNetworkServices(name string, fn func(*config.ServicesConfig) error) (status int, err error) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	for i := range s.cfg.IRC.Networks {
		if !strings.EqualFold(s.cfg.IRC.Networks[i].Name, name) {
			continue
		}
		svc := s.cfg.IRC.Networks[i].Services
		if err := fn(&svc); err != nil {
			return http.StatusBadRequest, err
		}
		s.cfg.IRC.Networks[i].Services = svc
		if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
			return http.StatusInternalServerError, err
		}
		return 0, nil
	}
	return http.StatusNotFound, errNetworkNotFound
}

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

const errNetworkNotFound = simpleErr("Network not found")

// handleIRCNickServ: NickServ actions for one network (dashboard-only, admin + CSRF).
// action: register | identify (send to NickServ now), save (store password for auto-identify
// on connect), clear (forget it). register/identify accept save:true to also store the password;
// identify with a blank password uses the stored one.
func (s *Server) handleIRCNickServ(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if isAdmin, _ := s.requireAdminCSRF(r); !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Network  string `json:"network"`
		Action   string `json:"action"`
		Password string `json:"password"`
		Email    string `json:"email"`
		Code     string `json:"code"`
		Save     bool   `json:"save"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	network := strings.TrimSpace(req.Network)

	persist := func(pw string) bool {
		if code, err := s.updateNetworkServices(network, func(v *config.ServicesConfig) error {
			v.NickServPassword = pw
			return nil
		}); err != nil {
			http.Error(w, err.Error(), code)
			return false
		}
		if err := s.runFullRehashFromWeb("web (nickserv password)"); err != nil {
			http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return false
		}
		return true
	}

	var replies []string
	switch req.Action {
	case "clear":
		if !persist("") {
			return
		}
	case "save":
		if req.Password == "" {
			http.Error(w, "password required", http.StatusBadRequest)
			return
		}
		if !persist(req.Password) {
			return
		}
	case "register", "identify", "verify":
		if s.bot == nil {
			http.Error(w, "Bot not running", http.StatusServiceUnavailable)
			return
		}
		pw := req.Password
		if pw == "" && req.Action == "identify" {
			s.cfgMu.RLock()
			if n, ok := config.FindIRCNetworkByName(s.cfg.IRC.Networks, network); ok {
				pw = n.Services.NickServPassword
			}
			s.cfgMu.RUnlock()
		}
		if req.Save && req.Password != "" && !persist(req.Password) {
			return
		}
		var ok bool
		var err error
		replies, ok, err = s.bot.NickServCommand(network, req.Action, pw, strings.TrimSpace(req.Email), strings.TrimSpace(req.Code))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Networks without NickServ log in via SASL: once the account exists, switch the
		// network to SASL PLAIN (account = the bot's nick) so the next connect authenticates.
		if ok && req.Action == "register" && req.Save && req.Password != "" {
			s.cfgMu.RLock()
			n, _ := config.FindIRCNetworkByName(s.cfg.IRC.Networks, network)
			s.cfgMu.RUnlock()
			if code, err := s.updateNetworkServices(network, func(v *config.ServicesConfig) error {
				v.Enabled, v.Mechanism, v.Username, v.Password = true, "plain", n.Nickname, req.Password
				return nil
			}); err != nil {
				http.Error(w, "Registered, but enabling SASL failed: "+err.Error(), code)
				return
			}
			replies = append(replies, "SASL PLAIN enabled for this network; reconnecting to authenticate.")
			defer func() { _ = s.runFullRehashFromWeb("web (sasl after register)") }()
		}
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "replies": replies})
}

// handleIRCNetworkCert manages a network's TLS client certificate (for CertFP / SASL EXTERNAL).
// action: generate (self-signed), upload (PEM cert + private key), delete. The PEM bundle is
// stored encrypted in config.yaml and never returned; only the SHA-256 fingerprint is.
func (s *Server) handleIRCNetworkCert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if isAdmin, _ := s.requireAdminCSRF(r); !isAdmin {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Network string `json:"network"`
		Action  string `json:"action"`
		PEM     string `json:"pem"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	network := strings.TrimSpace(req.Network)
	fp := ""
	code, err := s.updateNetworkServices(network, func(v *config.ServicesConfig) error {
		switch req.Action {
		case "generate":
			bundle, f, err := config.GenerateClientCert(network)
			if err != nil {
				return err
			}
			v.ClientCert, fp = bundle, f
		case "upload":
			f, err := config.ClientCertFingerprint(req.PEM)
			if err != nil {
				return err
			}
			v.ClientCert, fp = strings.TrimSpace(req.PEM)+"\n", f
		case "delete":
			v.ClientCert = ""
			if v.SASLExternal() {
				v.Enabled = false // EXTERNAL without a cert would fail validation and every connect
			}
		default:
			return simpleErr("unknown action")
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	if err := s.runFullRehashFromWeb("web (irc network cert)"); err != nil {
		http.Error(w, "Saved but failed to apply: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "fingerprint": fp})
}
