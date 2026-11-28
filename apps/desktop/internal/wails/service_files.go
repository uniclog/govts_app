package wailsui

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/wailsapp/wails/v3/pkg/application"
	"strconv"
	"strings"
	"time"
	"uniclog.io/sonoryx/apps/desktop/internal/filequeue"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/fileapi"
	"uniclog.io/sonoryx/internal/fileops"
)

// Wails' Windows file dialog returns an internal sentinel with this message.
// It is not exported by application, so normalize cancellation at the boundary.
func fileDialogCancelled(err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || strings.EqualFold(err.Error(), "cancelled by user"))
}

func fileTarget(kind, id string) (domain.ChatTarget, error) {
	t := domain.ChatTarget{}
	switch kind {
	case "channel":
		t.Kind = domain.ChatChannel
	case "direct":
		t.Kind = domain.ChatDirect
	default:
		return t, errors.New("invalid file target")
	}
	var err error
	t.ID, err = strconv.ParseInt(id, 10, 64)
	if err != nil {
		return t, err
	}
	return t, t.Validate()
}
func (s *Service) FileQueue(expected string) []filequeue.FileTask {
	return s.files.List(expected)
}
func (s *Service) FileAttached(expected string, ids []string) error {
	for _, t := range s.files.List(expected) {
		for _, id := range ids {
			if t.Direction == "upload" && strconv.FormatInt(t.AttachmentID, 10) == id && t.State == "ready" {
				if err := s.files.Control(expected, t.ID, "attached"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (s *Service) FileControl(expected, id, action string) error {
	return s.files.Control(expected, id, action)
}
func (s *Service) FileCapabilities(expected string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.FileTransfers().FileCapabilities(ctx, expected)
}
func (s *Service) FileAccountRights(expected, accountID string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.FileTransfers().FileAccountRights(ctx, expected, accountID)
}

func (s *Service) FileAdmin(expected, method, cursor string, input map[string]any) (map[string]any, error) {
	if method != "GET" && method != "POST" {
		return nil, errors.New("invalid method")
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.FileTransfers().FileAdmin(ctx, expected, method, cursor, input)
}
func (s *Service) FilePick(expected, kind, targetID string) ([]filequeue.FileTask, error) {
	target, err := fileTarget(kind, targetID)
	if err != nil {
		return nil, err
	}
	app := application.Get()
	if app == nil {
		return nil, errors.New("file dialog unavailable")
	}
	paths, err := app.Dialog.OpenFile().CanChooseFiles(true).PromptForMultipleSelection()
	if fileDialogCancelled(err) {
		return []filequeue.FileTask{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(paths) > domain.MaxChatAttachments {
		return nil, errors.New("maximum four attachments per message")
	}
	list := []filequeue.FileTask{}
	for _, path := range paths {
		t, e := s.files.Add(filequeue.FileTask{Context: expected, Kind: target.Kind, TargetID: target.ID, Direction: "upload", Path: path})
		if e != nil {
			return list, e
		}
		list = append(list, t)
	}
	return list, nil
}
func (s *Service) FileMetadata(expected string, ids []string) ([]fileapi.FileAttachment, error) {
	if len(ids) > 64 {
		return nil, errors.New("too many attachment IDs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.FileTransfers().FileMetadata(ctx, expected, ids)
}
func (s *Service) FilePreview(expected, id string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	data, err := s.client.FileTransfers().FilePreview(ctx, expected, id)
	if err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data), nil
}

// The injected Wails context cancels the download when the viewer closes.
func (s *Service) FileOriginalImage(ctx context.Context, expected, id string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	data, mime, err := s.client.FileTransfers().FileOriginalImage(ctx, expected, id)
	if err != nil {
		return "", err
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (s *Service) FileSave(expected, id string) (filequeue.FileTask, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	list, err := s.client.FileTransfers().FileMetadata(ctx, expected, []string{id})
	if err != nil {
		return filequeue.FileTask{}, err
	}
	if len(list) != 1 || list[0].Deleted {
		return filequeue.FileTask{}, errors.New("attachment unavailable")
	}
	att := list[0]
	app := application.Get()
	if app == nil {
		return filequeue.FileTask{}, errors.New("file dialog unavailable")
	}
	path, err := app.Dialog.SaveFile().SetFilename(fileops.SafeName(att.Name)).PromptForSingleSelection()
	if fileDialogCancelled(err) {
		return filequeue.FileTask{}, nil
	}
	if err != nil || path == "" {
		return filequeue.FileTask{}, err
	}
	return s.files.Add(filequeue.FileTask{Context: expected, Direction: "download", AttachmentID: att.ID, Name: att.Name, Size: att.Size, SHA256: att.SHA256, Path: path})
}

// Drop/paste data crosses the bridge in bounded chunks.
func (s *Service) FileStageCreate(expected string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	if _, err := s.client.FileTransfers().FileCapabilities(ctx, expected); err != nil {
		return "", err
	}
	return s.files.StageCreate()
}

func (s *Service) FileStageWrite(id string, offset int64, data string) error {
	if len(data) > 360000 || offset < 0 || offset > 1<<40 {
		return errors.New("invalid staged chunk")
	}
	dataBytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	return s.files.StageWrite(id, offset, dataBytes)
}
func (s *Service) FileStageFinish(expected, kind, targetID, id, name string) (filequeue.FileTask, error) {
	target, err := fileTarget(kind, targetID)
	if err != nil {
		return filequeue.FileTask{}, err
	}
	return s.files.StageFinish(id, filequeue.FileTask{Context: expected, Kind: target.Kind, TargetID: target.ID, Direction: "upload", Name: fileops.SafeName(name)})
}
func (s *Service) FileStageCancel(id string) error {
	return s.files.StageCancel(id)
}

func ConfigureFileQueue(s *Service, profileDir string) {
	s.files = filequeue.New(s.client.FileTransfers(), profileDir)
}

func CloseFileQueue(s *Service, ctx context.Context) error {
	if s.files == nil {
		return nil
	}
	return s.files.Close(ctx)
}
