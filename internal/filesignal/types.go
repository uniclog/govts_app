// Package filesignal holds the file-transfer contract shared by the HTTPS file
// server and the desktop client: JSON payloads, the chunk size and name rules.
package filesignal

import "uniclog.io/sonoryx/internal/domain"

// ChunkSize is the largest upload chunk and download range the server accepts.
const ChunkSize = 4 << 20

type FileUpload struct {
	ID           string          `json:"id"`
	RequestID    string          `json:"requestId"`
	UserID       int64           `json:"userId,string"`
	Kind         domain.ChatKind `json:"kind"`
	TargetID     int64           `json:"targetId,string"`
	Name         string          `json:"name"`
	Size         int64           `json:"size"`
	SHA256       string          `json:"sha256"`
	Offset       int64           `json:"offset"`
	State        string          `json:"state"`
	AttachmentID int64           `json:"attachmentId,string"`
	Updated      int64           `json:"updated"`
	TerminalAt   int64           `json:"-"`
	Error        string          `json:"error,omitempty"`
}

type FileAttachment struct {
	ID        int64           `json:"id,string"`
	UploadID  string          `json:"-"`
	UserID    int64           `json:"userId,string"`
	Kind      domain.ChatKind `json:"kind"`
	TargetID  int64           `json:"targetId,string"`
	Name      string          `json:"name"`
	Size      int64           `json:"size"`
	SHA256    string          `json:"sha256"`
	MessageID int64           `json:"messageId,string"`
	Deleted   bool            `json:"deleted"`
	Created   int64           `json:"created"`
}
