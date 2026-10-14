package uploads

import "database/sql"

// Column lists shared by the Upload queries; scanUpload reads them in exactly this order.
const (
	uploadHead = `id, ticket_id, username, title, description, content_path, expires_in_days, status, channel`
	uploadMeta = `COALESCE(upload_type,'paste'), COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(download_count, 0),
		       COALESCE(public_ref,''), COALESCE(paste_kind,''), COALESCE(client_host,''), COALESCE(md5_hex,''), COALESCE(sha256_hex,''),
		       COALESCE(is_public, 1)`

	approvedCols = uploadHead + `, approved_at, ` + uploadMeta
	pendingCols  = uploadHead + `, created_at, ` + uploadMeta
	ticketCols   = uploadHead + `, COALESCE(network,''), approved_at, ` + uploadMeta
)

// rowKind says which columns sit between uploadHead and uploadMeta.
type rowKind int

const (
	approvedRow rowKind = iota // approvedCols: approved_at
	pendingRow                 // pendingCols: created_at
	ticketRow                  // ticketCols: network, approved_at
)

type rowScanner interface{ Scan(dest ...any) error }

// scanUpload reads one row selected with approvedCols / pendingCols / ticketCols.
func scanUpload(sc rowScanner, kind rowKind) (*Upload, error) {
	var u Upload
	var ipub int
	dest := []any{&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel}
	switch kind {
	case approvedRow:
		dest = append(dest, &u.ApprovedAt)
	case pendingRow:
		dest = append(dest, &u.CreatedAt)
	case ticketRow:
		dest = append(dest, &u.Network, &u.ApprovedAt)
	}
	dest = append(dest, &u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes, &u.DownloadCount,
		&u.PublicRef, &u.PasteKind, &u.ClientHost, &u.MD5Hex, &u.SHA256Hex, &ipub)
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	u.IsPublic = ipub != 0
	return &u, nil
}

// scanUploads drains rows (the caller closes them).
func scanUploads(rows *sql.Rows, kind rowKind) ([]*Upload, error) {
	var list []*Upload
	for rows.Next() {
		u, err := scanUpload(rows, kind)
		if err != nil {
			return nil, err
		}
		list = append(list, u)
	}
	return list, rows.Err()
}
