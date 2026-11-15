package wailsui

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/wailsapp/wails/v3/pkg/application"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"uniclog.io/sonoryx/internal/clientapp"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/filesignal"
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
func (s *Service) FileQueue(expected string) []clientapp.FileTask {
	return s.client.Files().List(expected)
}
func (s *Service) FileAttached(expected string, ids []string) error {
	for _, t := range s.client.Files().List(expected) {
		for _, id := range ids {
			if t.Direction == "upload" && strconv.FormatInt(t.AttachmentID, 10) == id && t.State == "ready" {
				if err := s.client.Files().Control(expected, t.ID, "attached"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (s *Service) FileControl(expected, id, action string) error {
	return s.client.Files().Control(expected, id, action)
}
func (s *Service) FileCapabilities(expected string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.FileCapabilities(ctx, expected)
}
func (s *Service) FileAccountRights(expected, accountID string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.FileAccountRights(ctx, expected, accountID)
}

func (s *Service) FileAdmin(expected, method, cursor string, input map[string]any) (map[string]any, error) {
	if method != "GET" && method != "POST" {
		return nil, errors.New("invalid method")
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.FileAdmin(ctx, expected, method, cursor, input)
}
func (s *Service) FilePick(expected, kind, targetID string) ([]clientapp.FileTask, error) {
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
		return []clientapp.FileTask{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(paths) > domain.MaxChatAttachments {
		return nil, errors.New("maximum four attachments per message")
	}
	list := []clientapp.FileTask{}
	for _, path := range paths {
		t, e := s.client.Files().Add(clientapp.FileTask{Context: expected, Kind: target.Kind, TargetID: target.ID, Direction: "upload", Path: path})
		if e != nil {
			return list, e
		}
		list = append(list, t)
	}
	return list, nil
}
func (s *Service) FileMetadata(expected string, ids []string) ([]filesignal.FileAttachment, error) {
	if len(ids) > 64 {
		return nil, errors.New("too many attachment IDs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.FileMetadata(ctx, expected, ids)
}
func (s *Service) FilePreview(expected, id string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	data, err := s.client.FilePreview(ctx, expected, id)
	if err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data), nil
}

// The injected Wails context cancels the download when the viewer closes.
func (s *Service) FileOriginalImage(ctx context.Context, expected, id string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	data, mime, err := s.client.FileOriginalImage(ctx, expected, id)
	if err != nil {
		return "", err
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (s *Service) FileSave(expected, id string) (clientapp.FileTask, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	list, err := s.client.FileMetadata(ctx, expected, []string{id})
	if err != nil {
		return clientapp.FileTask{}, err
	}
	if len(list) != 1 || list[0].Deleted {
		return clientapp.FileTask{}, errors.New("attachment unavailable")
	}
	att := list[0]
	app := application.Get()
	if app == nil {
		return clientapp.FileTask{}, errors.New("file dialog unavailable")
	}
	path, err := app.Dialog.SaveFile().SetFilename(filesignal.SafeName(att.Name)).PromptForSingleSelection()
	if fileDialogCancelled(err) {
		return clientapp.FileTask{}, nil
	}
	if err != nil || path == "" {
		return clientapp.FileTask{}, err
	}
	return s.client.Files().Add(clientapp.FileTask{Context: expected, Direction: "download", AttachmentID: att.ID, Name: att.Name, Size: att.Size, SHA256: att.SHA256, Path: path})
}

// Drop/paste data crosses the bridge in bounded chunks.
func (s *Service) FileStageCreate(expected string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	if _, err := s.client.FileCapabilities(ctx, expected); err != nil {
		return "", err
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "Sonoryx", "file-staging")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "staged-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return filepath.Base(f.Name()), nil
}
func stagePath(id string) (string, error) {
	if filepath.Base(id) != id || len(id) > 80 || len(id) < 8 || id[:7] != "staged-" {
		return "", errors.New("invalid staged file")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Sonoryx", "file-staging", id), nil
}
func (s *Service) FileStageWrite(id string, offset int64, data string) error {
	if len(data) > 360000 || offset < 0 || offset > 1<<40 {
		return errors.New("invalid staged chunk")
	}
	path, err := stagePath(id)
	if err != nil {
		return err
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if stat.Size() != offset {
		return errors.New("staged offset conflict")
	}
	_, err = f.WriteAt(b, offset)
	return err
}
func (s *Service) FileStageFinish(expected, kind, targetID, id, name string) (clientapp.FileTask, error) {
	target, err := fileTarget(kind, targetID)
	if err != nil {
		return clientapp.FileTask{}, err
	}
	path, err := stagePath(id)
	if err != nil {
		return clientapp.FileTask{}, err
	}
	return s.client.Files().Add(clientapp.FileTask{Context: expected, Kind: target.Kind, TargetID: target.ID, Direction: "upload", Path: path, Name: filesignal.SafeName(name), Staged: true})
}
func (s *Service) FileStageCancel(id string) error {
	path, err := stagePath(id)
	if err != nil {
		return err
	}
	return os.Remove(path)
}
