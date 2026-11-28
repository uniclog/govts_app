package filetransfer

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/time/rate"
	"net/http"
	"strconv"
	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/apps/server/internal/voice"
	"uniclog.io/sonoryx/internal/domain"
)

func (s *Server) settings() Config { return *s.config.Load() }
func (s *Server) loadPolicy(ctx context.Context) error {
	data, err := s.store.FilePolicy(ctx)
	if err != nil {
		return err
	}
	if data == "" {
		return nil
	}
	c := s.settings()
	directory := c.Directory
	if err = json.Unmarshal([]byte(data), &c); err != nil {
		return err
	}
	c.Directory = directory
	if err = c.Validate(); err != nil {
		return err
	}
	s.config.Store(&c)
	s.inbound.SetLimit(rate.Limit(c.ServerBytesPerSecond))
	s.outbound.SetLimit(rate.Limit(c.ServerBytesPerSecond))
	return nil
}
func (s *Server) allowed(ctx context.Context, a voice.Session, upload bool) bool {
	c := s.settings()
	up, down, err := s.store.FileRights(ctx, a.UserID)
	if err != nil {
		return false
	}
	if upload {
		return c.UploadsEnabled && up
	}
	return c.DownloadsEnabled && down
}
func (s *Server) admin(w http.ResponseWriter, r *http.Request, a voice.Session, u *userLimit) {
	account, err := s.store.Account(r.Context(), a.UserID)
	if err != nil || (!account.Owner && account.Permissions&persist.PermissionAdmin == 0) {
		s.reject(w, 403, "forbidden")
		return
	}
	actor := strconv.FormatInt(a.UserID, 10)
	switch {
	case r.Method == "GET" && r.URL.Path == "/files/v1/admin/rights":
		id, e := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if e != nil || id <= 0 {
			s.reject(w, 400, "invalid_id")
			return
		}
		if _, e = s.store.Account(r.Context(), id); e != nil {
			s.reject(w, 404, "unavailable")
			return
		}
		upload, download, e := s.store.FileRights(r.Context(), id)
		if e != nil {
			s.reject(w, 500, "storage_error")
			return
		}
		jsonResponse(w, map[string]bool{"upload": upload, "download": download})
	case r.Method == "GET" && r.URL.Path == "/files/v1/admin":
		cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
		list, err := s.store.FileAdminList(r.Context(), cursor)
		if err != nil {
			s.reject(w, 500, "storage_error")
			return
		}
		usage, err := s.store.FileUsage(r.Context())
		if err != nil {
			s.reject(w, 500, "storage_error")
			return
		}
		free, err := freeBytes(s.settings().Directory)
		if err != nil {
			s.reject(w, 500, "storage_error")
			return
		}
		c := s.settings()
		c.Directory = ""
		jsonResponse(w, map[string]any{"config": c, "files": list, "usage": usage, "freeBytes": free, "metrics": s.Metrics()})
	case r.Method == "POST" && r.URL.Path == "/files/v1/admin":
		var request struct {
			Action   string `json:"action"`
			Config   Config `json:"config"`
			ID       string `json:"id"`
			Upload   bool   `json:"upload"`
			Download bool   `json:"download"`
		}
		if !decode(w, r, &request) {
			s.reject(w, 400, "invalid_request")
			return
		}
		// Never hold the shared voice policy gate while reading from the network.
		s.policyGate.Lock()
		defer s.policyGate.Unlock()
		account, err = s.store.Account(r.Context(), a.UserID)
		if err != nil || account.Banned || (!account.Owner && account.Permissions&persist.PermissionAdmin == 0) || s.check(r.Context(), a, domain.ChatTarget{}) != nil {
			s.reject(w, 403, "forbidden")
			return
		}
		adminActor := voice.AdminActor{UserID: account.ID, JoinLevel: account.JoinLevel, Permissions: account.Permissions, Owner: account.Owner}
		switch request.Action {
		case "policy":
			c := request.Config
			if c.Revision != s.settings().Revision {
				s.reject(w, 409, "policy_conflict")
				return
			}
			c.Revision++
			c.Directory = s.settings().Directory
			if c.Validate() != nil {
				s.reject(w, 400, "invalid_policy")
				return
			}
			data, _ := json.Marshal(c)
			if err = s.store.SaveFilePolicy(r.Context(), actor, string(data)); err == nil {
				s.mu.Lock()
				s.inbound.SetLimit(rate.Limit(c.ServerBytesPerSecond))
				s.outbound.SetLimit(rate.Limit(c.ServerBytesPerSecond))
				for _, u := range s.users {
					u.in.SetLimit(rate.Limit(c.UserBytesPerSecond))
					u.out.SetLimit(rate.Limit(c.UserBytesPerSecond))
				}
				s.config.Store(&c)
				s.mu.Unlock()
			}
		case "delete":
			id, e := strconv.ParseInt(request.ID, 10, 64)
			if e != nil || id <= 0 {
				s.reject(w, 400, "invalid_id")
				return
			}
			err = s.administration.DeleteAttachment(r.Context(), adminActor, id)
		case "rights":
			id, e := strconv.ParseInt(request.ID, 10, 64)
			if e != nil || id <= 0 {
				s.reject(w, 400, "invalid_id")
				return
			}
			err = s.administration.SetFileRights(r.Context(), adminActor, id, request.Upload, request.Download)
		default:
			s.reject(w, 400, "invalid_action")
			return
		}
		if err != nil {
			if errors.Is(err, voice.ErrPermissionDenied) {
				s.reject(w, 403, "forbidden")
				return
			}
			s.reject(w, 409, "operation_failed")
			return
		}
		w.WriteHeader(204)
	default:
		s.reject(w, 404, "unavailable")
	}
}
