package fileapi

import "uniclog.io/sonoryx/internal/domain"

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
	Error        string          `json:"error,omitempty"`
}

type FileAttachment struct {
	ID        int64           `json:"id,string"`
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
