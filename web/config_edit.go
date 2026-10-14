package web

import (
	"net/http"

	"botIAask/config"
)

// mutateConfig applies fn to the dashboard's working copy of the config under cfgMu and saves it.
// fn rejects an edit by returning (http status, error). A rejected or unsaved edit is rolled back,
// so the in-memory config never drifts from what is on disk (handlers used to leave a half-applied
// edit behind after a validation or save failure, and the next unrelated save then wrote it out).
// It reports whether the edit was saved; on false the error response has already been written.
func (s *Server) mutateConfig(w http.ResponseWriter, fn func(cfg *config.Config) (status int, err error)) bool {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	backup, cloneErr := config.CloneConfig(s.cfg)
	rollback := func() {
		if cloneErr == nil {
			*s.cfg = *backup // in place: fetchers share this pointer and must not keep the rejected edit
		}
	}
	if status, err := fn(s.cfg); err != nil {
		rollback()
		http.Error(w, err.Error(), status)
		return false
	}
	if err := config.SaveConfig(config.DefaultConfigPath, s.cfg); err != nil {
		rollback()
		http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
		return false
	}
	return true
}

// adminList returns the admin hostmask slice an edit targets: the global admin.admins for an empty
// network, otherwise that network's own list. ok is false for an unknown network.
func adminList(cfg *config.Config, network string) (list *[]string, ok bool) {
	if network == "" {
		return &cfg.Admin.Admins, true
	}
	i := config.IRCNetworkIndex(cfg.IRC.Networks, network)
	if i < 0 {
		return nil, false
	}
	return &cfg.IRC.Networks[i].Admins, true
}
