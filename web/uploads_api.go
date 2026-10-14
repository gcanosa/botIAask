package web

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"botIAask/uploads"
)

func (s *Server) effectiveMaxFileMB() int {
	mb := s.getConfig().Uploads.MaxFileMB
	if mb <= 0 {
		return 200
	}
	return mb
}

func (s *Server) maxUploadBytes() int64 {
	return int64(s.effectiveMaxFileMB()) * 1024 * 1024
}

// multipartSlack adds headroom for multipart boundaries and small form fields.
const multipartSlack int64 = 1 << 20

func parseUploadToken(r *http.Request) string {
	raw := strings.TrimSpace(r.URL.Query().Get("token"))
	if raw == "" {
		return ""
	}
	if dec, err := url.QueryUnescape(raw); err == nil {
		raw = strings.TrimSpace(dec)
	}
	return raw
}

// maxPasteFormBytes caps a pasted-text submission (body + form overhead).
const maxPasteFormBytes = 2 << 20

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if s.uploadsDB == nil {
		http.Error(w, "Uploads are not configured on this server", http.StatusServiceUnavailable)
		return
	}
	token := parseUploadToken(r)
	if token == "" {
		http.Error(w, "Token is required", http.StatusBadRequest)
		return
	}

	upload, err := s.uploadsDB.GetUploadByToken(token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("upload: no session for token (len=%d); ensure web and IRC use the same uploads DB (see uploads.db_path in config)", len(token))
		} else {
			log.Printf("upload: token lookup failed: %v", err)
		}
		http.Error(w, "Invalid or expired token", http.StatusNotFound)
		return
	}

	if upload.Status != "pending_form" {
		http.Error(w, "This token has already been used", http.StatusBadRequest)
		return
	}

	if time.Since(upload.CreatedAt) > uploads.SessionTTL {
		http.Error(w, "This token has expired", http.StatusBadRequest)
		return
	}

	if upload.IsFile() {
		s.handleUploadFile(w, r, token, upload)
		return
	}

	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err := s.templates.ExecuteTemplate(w, "upload.html", map[string]interface{}{
			"Upload":  upload,
			"BaseURL": s.getConfig().Web.BaseURL,
		})
		if err != nil {
			log.Printf("ExecuteTemplate upload.html error: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if r.Method == http.MethodPost {
		// /upload is exempt from secure()'s body cap (file uploads); pastes are text, so cap here.
		r.Body = http.MaxBytesReader(w, r.Body, maxPasteFormBytes)
		if err := r.ParseMultipartForm(maxPasteFormBytes); err != nil && !errors.Is(err, http.ErrNotMultipart) {
			http.Error(w, "Paste too large", http.StatusRequestEntityTooLarge)
			return
		}
		title := r.FormValue("title")
		desc := r.FormValue("description")
		content := r.FormValue("content")
		expiresStr := r.FormValue("expires")

		expiresDays := parseExpiryDays(expiresStr)

		var ticketID string
		var err error
		for attempt := 0; attempt < ticketIDAttempts; attempt++ {
			ticketID = newTicketID()
			err = s.uploadsDB.SubmitUpload(token, ticketID, title, desc, content, expiresDays, s.clientHostFromRequest(r))
			if !errors.Is(err, uploads.ErrTicketIDTaken) {
				break
			}
		}
		if errors.Is(err, uploads.ErrTokenUsed) {
			http.Error(w, "This upload link was already used", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.bot.SendMessage(upload.Network, upload.Channel, fmt.Sprintf("\x0307[UPLOAD]\x03 New ticket pending approval: %s (by %s)", ticketID, upload.Username))
		s.bot.NotifyAdmins(fmt.Sprintf("\x0307[TICKET]\x03 New pending approval: %s. Use !ticket approve %s to publish.", ticketID, ticketID))

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#0f172a;color:white;display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;'>")
		fmt.Fprintf(w, "<h1 style='color:#38bdf8;'>Successfully submitted</h1>")
		fmt.Fprintf(w, "<p>Ticket ID: <strong>%s</strong></p>", ticketID)
		fmt.Fprintf(w, "<p>Please wait for admin approval in IRC.</p>")
		fmt.Fprintf(w, "</body></html>")
	}
}

// isAllowedFileType validates file uploads to prevent dangerous file types.
func isAllowedFileType(filename, contentType string) bool {
	blockedExts := map[string]bool{
		".exe": true, ".bat": true, ".cmd": true, ".com": true, ".scr": true,
		".vbs": true, ".js": true, ".jar": true, ".app": true, ".deb": true, ".rpm": true,
		".sh": true, ".bash": true, ".ps1": true, ".py": true, ".rb": true,
	}

	blockedMimes := map[string]bool{
		"application/x-executable":    true,
		"application/x-msdownload":    true,
		"application/x-msdos-program": true,
		"application/x-sh":            true,
		"application/x-shellscript":   true,
		"application/x-perl":          true,
		"application/x-python":        true,
		"application/x-ruby":          true,
	}

	ext := strings.ToLower(filepath.Ext(filename))
	if blockedExts[ext] {
		return false
	}

	ct := strings.ToLower(strings.SplitN(contentType, ";", 2)[0])
	if blockedMimes[ct] {
		return false
	}

	return true
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request, token string, sess *uploads.Upload) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err := s.templates.ExecuteTemplate(w, "file_upload.html", map[string]interface{}{
			"Upload":    sess,
			"BaseURL":   s.getConfig().Web.BaseURL,
			"MaxFileMB": s.effectiveMaxFileMB(),
		})
		if err != nil {
			log.Printf("ExecuteTemplate file_upload.html error: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := s.maxUploadBytes() + multipartSlack
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	mem := int64(32 << 20)
	if mem > limit {
		mem = limit
	}
	if err := r.ParseMultipartForm(mem); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) || errors.Is(err, io.ErrUnexpectedEOF) {
			http.Error(w, "Upload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Bad multipart form", http.StatusBadRequest)
		return
	}

	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "File is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if hdr.Size > s.maxUploadBytes() {
		http.Error(w, "File too large", http.StatusRequestEntityTooLarge)
		return
	}

	title := r.FormValue("title")
	if strings.TrimSpace(title) == "" {
		http.Error(w, "Title is required", http.StatusBadRequest)
		return
	}
	desc := r.FormValue("description")
	expiresDays := parseExpiryDays(r.FormValue("expires"))

	ext := uploads.SafeFileExt(hdr.Filename)
	// O_EXCL: a random id that happens to match an existing file must never overwrite it.
	var ticketID, diskPath string
	var out *os.File
	for attempt := 0; attempt < ticketIDAttempts && out == nil; attempt++ {
		ticketID = newTicketID()
		diskPath = filepath.Join(s.uploadsDB.FilesDiskDir(), ticketID+ext)
		f, err := os.OpenFile(diskPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			out = f
		} else if !errors.Is(err, fs.ErrExist) {
			http.Error(w, "Could not store file", http.StatusInternalServerError)
			return
		}
	}
	if out == nil {
		http.Error(w, "Could not allocate a ticket id, try again", http.StatusServiceUnavailable)
		return
	}
	n, err := io.Copy(out, io.LimitReader(file, s.maxUploadBytes()+1))
	out.Close()
	if err != nil {
		os.Remove(diskPath)
		http.Error(w, "Error saving file", http.StatusInternalServerError)
		return
	}
	if n > s.maxUploadBytes() {
		os.Remove(diskPath)
		http.Error(w, "File too large", http.StatusRequestEntityTooLarge)
		return
	}
	if n == 0 {
		os.Remove(diskPath)
		http.Error(w, "Empty file", http.StatusBadRequest)
		return
	}

	ctype := hdr.Header.Get("Content-Type")
	if ctype == "" || ctype == "application/octet-stream" {
		ctype = ""
	}

	if !isAllowedFileType(hdr.Filename, ctype) {
		os.Remove(diskPath)
		http.Error(w, "File type not allowed", http.StatusBadRequest)
		return
	}

	mdH, shH, err := uploads.HexMD5SHA256FromFile(diskPath)
	if err != nil {
		os.Remove(diskPath)
		http.Error(w, "Error hashing file", http.StatusInternalServerError)
		return
	}

	err = s.uploadsDB.SubmitFileUpload(token, ticketID, title, desc, expiresDays, diskPath, hdr.Filename, ctype, n, s.clientHostFromRequest(r), mdH, shH)
	if err != nil {
		os.Remove(diskPath)
		if errors.Is(err, uploads.ErrTokenUsed) {
			http.Error(w, "This upload link was already used", http.StatusConflict)
			return
		}
		if errors.Is(err, uploads.ErrTicketIDTaken) {
			http.Error(w, "Could not allocate a ticket id, try again", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.bot.SendMessage(sess.Network, sess.Channel, fmt.Sprintf("\x0307[UPLOAD]\x03 New file pending approval: %s (by %s)", ticketID, sess.Username))
	s.bot.NotifyAdmins(fmt.Sprintf("\x0307[TICKET]\x03 New file pending approval: %s. Use !ticket approve %s to publish.", ticketID, ticketID))

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#0f172a;color:white;display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;'>")
	fmt.Fprintf(w, "<h1 style='color:#38bdf8;'>Successfully submitted</h1>")
	fmt.Fprintf(w, "<p>Ticket ID: <strong>%s</strong></p>", ticketID)
	fmt.Fprintf(w, "<p>Please wait for admin approval.</p>")
	fmt.Fprintf(w, "</body></html>")
}

func (s *Server) handleUploadCancel(w http.ResponseWriter, r *http.Request) {
	if s.uploadsDB == nil {
		http.Error(w, "Uploads are not configured on this server", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	username, channel, network, err := s.uploadsDB.CancelUploadByToken(token)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Nothing to cancel (unknown token or already submitted)", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Error cancelling", http.StatusInternalServerError)
		return
	}
	s.bot.SendMessage(network, channel, fmt.Sprintf("\x0304[CANCEL]\x03 User %s cancelled their upload session.", username))

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#0f172a;color:white;display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;'>")
	fmt.Fprintf(w, "<h1 style='color:#f87171;'>Session Cancelled</h1>")
	fmt.Fprintf(w, "<p>The bot has been notified.</p>")
	fmt.Fprintf(w, "</body></html>")
}

func (s *Server) handlePasteView(w http.ResponseWriter, r *http.Request) {
	ticketID := strings.TrimPrefix(r.URL.Path, "/p/")
	if ticketID == "" {
		http.NotFound(w, r)
		return
	}

	upload, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if upload.IsFile() {
		http.NotFound(w, r)
		return
	}

	if upload.Status != "approved" {
		http.Error(w, "This paste is pending approval or was cancelled", http.StatusForbidden)
		return
	}

	if upload.IsAccessExpired(time.Now()) && !s.staffAdminFromRequest(r) {
		http.Error(w, "This paste has expired", http.StatusGone)
		return
	}

	if !s.pathWithinDir(upload.ContentPath, s.uploadsDB.PastesDiskDir()) {
		http.Error(w, "Invalid content path", http.StatusInternalServerError)
		return
	}
	content, err := os.ReadFile(upload.ContentPath)
	if err != nil {
		http.Error(w, "Error reading content", http.StatusInternalServerError)
		return
	}

	data := struct {
		Upload  *uploads.Upload
		Content string
	}{
		Upload:  upload,
		Content: string(content),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err = s.templates.ExecuteTemplate(w, "paste.html", data)
	if err != nil {
		log.Printf("ExecuteTemplate paste.html error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// approvedUploadListJSON adds public-expiry fields for dashboard paste/file tables.
type approvedUploadListJSON struct {
	*uploads.Upload
	ExpiresAt *string `json:"expires_at,omitempty"`
	IsExpired bool    `json:"is_expired"`
}

func (s *Server) handlePastesList(w http.ResponseWriter, r *http.Request) {
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}

	pageStr := r.URL.Query().Get("page")
	page := 1
	if p, err := fmt.Sscanf(pageStr, "%d", &page); err == nil && p > 0 {
		if page < 1 {
			page = 1
		}
	}

	limit := 10
	offset := (page - 1) * limit

	staff := s.staffAdminFromRequest(r)
	items, total, err := s.uploadsDB.GetApprovedPastes(limit, offset, time.Now(), !staff)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if !staff {
		redacted := make([]*uploads.Upload, len(items))
		for i, u := range items {
			c := *u
			c.ClientHost = ""
			redacted[i] = &c
		}
		items = redacted
	}

	now := time.Now()
	out := make([]approvedUploadListJSON, len(items))
	for i, u := range items {
		row := approvedUploadListJSON{Upload: u, IsExpired: u.IsAccessExpired(now)}
		if exp, ok := u.AccessExpiresAt(); ok {
			expStr := exp.UTC().Format(time.RFC3339)
			row.ExpiresAt = &expStr
		}
		out[i] = row
	}

	totalPages := (total + limit - 1) / limit

	response := map[string]interface{}{
		"pastes":      out,
		"page":        page,
		"total_pages": totalPages,
		"total_count": total,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handlePasteDelete(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("admin_session")
	if err != nil || !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.csrfValid(r, cookie.Value) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ticketID := r.URL.Query().Get("ticketID")
	if ticketID == "" {
		http.Error(w, "ticketID required", http.StatusBadRequest)
		return
	}

	if err := s.uploadsDB.DeletePaste(ticketID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePendingPastes(w http.ResponseWriter, r *http.Request) {
	if !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}

	items, err := s.uploadsDB.GetPendingPastes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	json.NewEncoder(w).Encode(items)
}

func (s *Server) handlePasteApprove(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("admin_session")
	if err != nil || !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.csrfValid(r, cookie.Value) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ticketID := r.URL.Query().Get("ticketID")
	if ticketID == "" {
		http.Error(w, "ticketID required", http.StatusBadRequest)
		return
	}

	if err := s.uploadsDB.ApproveTicket(ticketID); err != nil {
		if errors.Is(err, uploads.ErrNotPending) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	upload, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err == nil {
		pubURL := fmt.Sprintf("%s/p/%s", s.getConfig().Web.BaseURL, ticketID)
		if upload.IsFile() {
			pubURL = fmt.Sprintf("%s/f/%s", s.getConfig().Web.BaseURL, ticketID)
		}
		s.bot.SendMessage(upload.Network, upload.Channel, fmt.Sprintf("\x0303[APPROVED]\x03 Ticket %s has been approved and published: %s", ticketID, pubURL))
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePasteReject(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("admin_session")
	if err != nil || !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.csrfValid(r, cookie.Value) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ticketID := r.URL.Query().Get("ticketID")
	if ticketID == "" {
		http.Error(w, "ticketID required", http.StatusBadRequest)
		return
	}

	if err := s.uploadsDB.CancelTicket(ticketID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Notify IRC
	upload, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err == nil {
		s.bot.SendMessage(upload.Network, upload.Channel, fmt.Sprintf("\x0304[REJECTED]\x03 Ticket %s was rejected by an administrator.", ticketID))
	}
	w.WriteHeader(http.StatusOK)
}

func contentDispositionAttachment(orig, ticketID, pathExt string) string {
	name := filepath.Base(orig)
	if name == "" || name == "." || name == "/" {
		if pathExt != "" {
			name = ticketID + pathExt
		} else {
			name = ticketID
		}
	}
	name = strings.ReplaceAll(name, `"`, `'`)
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	if len(name) > 180 {
		name = name[:180]
	}
	return fmt.Sprintf(`attachment; filename="%s"`, name)
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ticketID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/f/"), "/")
	if ticketID == "" {
		http.NotFound(w, r)
		return
	}
	upload, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !upload.IsFile() {
		http.NotFound(w, r)
		return
	}
	if upload.Status != "approved" {
		http.Error(w, "This file is pending approval or was cancelled", http.StatusForbidden)
		return
	}
	if !upload.IsPublic && !s.staffAdminFromRequest(r) {
		http.Error(w, "This file is not publicly accessible", http.StatusForbidden)
		return
	}
	if upload.IsAccessExpired(time.Now()) && !s.staffAdminFromRequest(r) {
		http.Error(w, "This file has expired", http.StatusGone)
		return
	}
	if !s.pathWithinDir(upload.ContentPath, s.uploadsDB.FilesDiskDir()) {
		http.Error(w, "Invalid file path", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(upload.ContentPath)
	if err != nil {
		http.Error(w, "Error reading file", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	ctype := upload.ContentType
	if ctype == "" {
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		ctype = http.DetectContentType(buf[:n])
		_, _ = f.Seek(0, 0)
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", contentDispositionAttachment(upload.OriginalFilename, ticketID, filepath.Ext(upload.ContentPath)))
	_, copyErr := io.Copy(w, f)
	if copyErr != nil {
		log.Printf("file download %s: %v", ticketID, copyErr)
		return
	}
	if err := s.uploadsDB.IncrementFileDownloadCount(ticketID); err != nil {
		log.Printf("increment download count %s: %v", ticketID, err)
	}
}

func (s *Server) handleUploadsFilesList(w http.ResponseWriter, r *http.Request) {
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pageStr := r.URL.Query().Get("page")
	page := 1
	if p, err := fmt.Sscanf(pageStr, "%d", &page); err == nil && p > 0 {
		if page < 1 {
			page = 1
		}
	}
	limit := 10
	offset := (page - 1) * limit
	publicOnly := !s.staffAdminFromRequest(r)
	items, total, err := s.uploadsDB.GetApprovedFiles(limit, offset, publicOnly)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if publicOnly {
		for _, u := range items {
			u.ClientHost = ""
		}
	}
	now := time.Now()
	fileRows := make([]approvedUploadListJSON, len(items))
	for i, u := range items {
		row := approvedUploadListJSON{Upload: u, IsExpired: u.IsAccessExpired(now)}
		if exp, ok := u.AccessExpiresAt(); ok {
			expStr := exp.UTC().Format(time.RFC3339)
			row.ExpiresAt = &expStr
		}
		fileRows[i] = row
	}
	totalPages := (total + limit - 1) / limit
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"files":       fileRows,
		"page":        page,
		"total_pages": totalPages,
		"total_count": total,
	})
}

func (s *Server) handleUploadsFilesPending(w http.ResponseWriter, r *http.Request) {
	if !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := s.uploadsDB.GetPendingFiles()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	json.NewEncoder(w).Encode(items)
}

func uploadAlreadyCompressed(u *uploads.Upload) bool {
	lower := strings.ToLower(u.OriginalFilename)
	if strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".tar.gz") {
		return true
	}
	ct := strings.ToLower(u.ContentType)
	return strings.Contains(ct, "gzip") || strings.Contains(ct, "x-gzip")
}

func uploadDetailCanCompress(u *uploads.Upload) bool {
	return u.IsFile() && !uploadAlreadyCompressed(u)
}

func writeSingleFileTgz(dstPath, srcPath, memberName string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file")
	}
	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = dst.Close()
		if cleanup {
			_ = os.Remove(dstPath)
		}
	}()
	gw := gzip.NewWriter(dst)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{
		Name:    memberName,
		Mode:    0644,
		Size:    st.Size(),
		ModTime: st.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		_ = tw.Close()
		_ = gw.Close()
		return err
	}
	if _, err := io.Copy(tw, src); err != nil {
		_ = tw.Close()
		_ = gw.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		_ = gw.Close()
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (s *Server) handleUploadDetail(w http.ResponseWriter, r *http.Request) {
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ticketID := strings.TrimSpace(r.URL.Query().Get("ticketID"))
	if ticketID == "" {
		http.Error(w, "ticketID required", http.StatusBadRequest)
		return
	}
	u, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if u.Status != "approved" && u.Status != "pending_approval" {
		http.NotFound(w, r)
		return
	}
	staff := s.staffAdminFromRequest(r)
	if u.Status == "pending_approval" && !staff {
		http.NotFound(w, r)
		return
	}
	if u.Status == "approved" && u.IsFile() && !u.IsPublic && !staff {
		http.NotFound(w, r)
		return
	}
	displayName := u.OriginalFilename
	if displayName == "" {
		displayName = filepath.Base(u.ContentPath)
	}
	now := time.Now()
	expired := u.IsAccessExpired(now)
	var viewPath, downloadPath *string
	if u.Status == "approved" {
		if u.IsFile() {
			p := "/f/" + u.TicketID
			if staff || !expired {
				downloadPath = &p
			}
		} else {
			p := "/p/" + u.TicketID
			if staff || !expired {
				viewPath = &p
			}
		}
	}
	var approvedAt *string
	if u.ApprovedAt.Valid {
		s := u.ApprovedAt.Time.UTC().Format(time.RFC3339)
		approvedAt = &s
	}
	var expiresAt *string
	if exp, ok := u.AccessExpiresAt(); ok {
		s := exp.UTC().Format(time.RFC3339)
		expiresAt = &s
	}
	clientHost := u.ClientHost
	if !staff {
		clientHost = ""
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ticket_id":         u.TicketID,
		"public_ref":        u.PublicRef,
		"paste_kind":        u.PasteKind,
		"upload_type":       u.UploadType,
		"status":            u.Status,
		"display_filename":  displayName,
		"title":             u.Title,
		"username":          u.Username,
		"client_host":       clientHost,
		"approved_at":       approvedAt,
		"expires_in_days":   u.ExpiresInDays,
		"expires_at":        expiresAt,
		"is_expired":        expired,
		"size_bytes":        u.SizeBytes,
		"md5_hex":           u.MD5Hex,
		"sha256_hex":        u.SHA256Hex,
		"is_file":           u.IsFile(),
		"is_public":         u.IsPublic,
		"can_compress":      staff && u.Status == "approved" && uploadDetailCanCompress(u),
		"download_path":     downloadPath,
		"view_path":         viewPath,
		"download_count":    u.DownloadCount,
		"original_filename": u.OriginalFilename,
	})
}

func (s *Server) handleUploadsPublic(w http.ResponseWriter, r *http.Request) {
	if !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TicketID string `json:"ticket_id"`
		Public   bool   `json:"is_public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	ticketID := strings.TrimSpace(req.TicketID)
	if ticketID == "" {
		http.Error(w, "ticket_id required", http.StatusBadRequest)
		return
	}
	if err := s.uploadsDB.SetFileIsPublic(ticketID, req.Public); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleUploadFileCompress(w http.ResponseWriter, r *http.Request) {
	if !s.staffAdminFromRequest(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.uploadsDB == nil {
		http.Error(w, "Uploads database not initialized", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ticketID := strings.TrimSpace(r.URL.Query().Get("ticketID"))
	if ticketID == "" {
		http.Error(w, "ticketID required", http.StatusBadRequest)
		return
	}
	u, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !u.IsFile() || u.Status != "approved" {
		http.Error(w, "Not an approved file", http.StatusBadRequest)
		return
	}
	if !uploadDetailCanCompress(u) {
		http.Error(w, "File is already compressed", http.StatusBadRequest)
		return
	}
	oldPath := u.ContentPath
	filesDir := s.uploadsDB.FilesDiskDir()
	if !s.pathWithinDir(oldPath, filesDir) {
		http.Error(w, "Invalid file path", http.StatusInternalServerError)
		return
	}
	if _, err := os.Stat(oldPath); err != nil {
		http.Error(w, "File missing on disk", http.StatusNotFound)
		return
	}
	baseName := strings.TrimSpace(u.OriginalFilename)
	if baseName == "" {
		baseName = filepath.Base(oldPath)
	}
	memberName := filepath.Base(baseName)
	if memberName == "" || memberName == "." {
		memberName = ticketID + filepath.Ext(oldPath)
	}
	newName := baseName + ".tgz"
	newPath := filepath.Join(filesDir, ticketID+".tgz")
	// One compress per ticket at a time: a double-click must not race on newPath/oldPath.
	if _, busy := s.compressing.LoadOrStore(ticketID, struct{}{}); busy {
		http.Error(w, "Compression already in progress", http.StatusConflict)
		return
	}
	defer s.compressing.Delete(ticketID)
	if err := writeSingleFileTgz(newPath, oldPath, memberName); err != nil {
		log.Printf("compress %s: %v", ticketID, err)
		_ = os.Remove(newPath)
		http.Error(w, "Compress failed", http.StatusInternalServerError)
		return
	}
	// Point the DB at the archive first, and only then drop the original, so a failure at any
	// step never leaves the ticket without a file.
	mdH, shH, err := uploads.HexMD5SHA256FromFile(newPath)
	if err != nil {
		_ = os.Remove(newPath)
		http.Error(w, "Hash error", http.StatusInternalServerError)
		return
	}
	st, err := os.Stat(newPath)
	if err != nil {
		_ = os.Remove(newPath)
		http.Error(w, "Stat error", http.StatusInternalServerError)
		return
	}
	if err := s.uploadsDB.ReplaceApprovedFileContent(ticketID, newPath, newName, "application/gzip", st.Size(), mdH, shH); err != nil {
		_ = os.Remove(newPath)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Remove(oldPath); err != nil {
		log.Printf("compress remove old %s: %v", ticketID, err)
	}
	u2, err := s.uploadsDB.GetUploadByTicketID(ticketID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(u2)
}

// ticketIDAttempts is how often a submission retries after a (very unlikely) id collision.
const ticketIDAttempts = 5

// newTicketID is a 48-bit random id (12 hex chars): it names the stored file and is the only
// secret in the /p/<id> and /f/<id> URLs, so 32 bits were both collision-prone and guessable.
func newTicketID() string { return generateHex(6) }

// parseExpiryDays reads the form's expiry in days: default 7, clamped to 0..MaxExpiryDays
// (0 = never expires).
func parseExpiryDays(s string) int {
	days := 7
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &days); err != nil {
		return 7
	}
	return min(max(days, 0), uploads.MaxExpiryDays)
}
