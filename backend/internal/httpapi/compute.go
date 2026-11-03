package httpapi

import (
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/compute"
	"github.com/icaruszezen/trainmaster/backend/internal/hostinfo"
)

// Compute API: /api/v1/compute. The browser reads the server machine and
// the training nodes; the nodes themselves register and send heartbeats
// under /agent, with the TM_NODE_TOKEN bearer token when one is set. The
// JSON shapes are defined by the types in internal/compute and
// internal/hostinfo; this file maps HTTP onto compute.Service. The task
// calls of the nodes (claiming and executing model conversions) are under
// /agent too; their handlers are in models.go.

func (s *server) computeRoutes(r chi.Router) {
	if s.host != nil {
		r.Get("/server", s.getComputeServer)
	}
	r.Route("/nodes", func(r chi.Router) {
		r.Get("/", s.listComputeNodes)
		r.Route("/{nodeID}", func(r chi.Router) {
			r.Get("/", s.getComputeNode)
			r.Delete("/", s.deleteComputeNode)
			r.Post("/probe", s.probeComputeNode)
		})
	})
	r.Route("/agent", func(r chi.Router) {
		r.Use(s.requireNodeToken)
		r.Post("/register", s.registerComputeNode)
		r.Post("/heartbeat", s.computeNodeHeartbeat)
		if s.models != nil {
			s.agentTaskRoutes(r)
		}
	})
}

// computeError writes the response for an error returned by
// compute.Service.
func (s *server) computeError(w http.ResponseWriter, r *http.Request, err error) {
	var ce *compute.Error
	if errors.As(err, &ce) {
		status, code := computeErrorStatus(ce.Kind)
		writeError(w, status, code, ce.Message)
		return
	}
	s.unexpectedError(w, r, err)
}

func computeErrorStatus(kind error) (int, string) {
	switch kind {
	case compute.ErrNotFound:
		return http.StatusNotFound, codeNotFound
	case compute.ErrUnknownNode:
		return http.StatusNotFound, codeUnknownNode
	case compute.ErrInvalid:
		return http.StatusUnprocessableEntity, codeValidationFailed
	case compute.ErrConflict:
		return http.StatusConflict, codeConflict
	}
	return http.StatusInternalServerError, codeInternal
}

// computeNodeID parses {nodeID}. It answers 404 for invalid IDs.
func computeNodeID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathID(r, "nodeID")
	if !ok {
		notFoundMessage(w, "节点不存在")
	}
	return id, ok
}

// computeServerInfo is the server machine plus how nodes connect to it.
type computeServerInfo struct {
	hostinfo.Server
	NodeRegistration nodeRegistration `json:"node_registration"`
}

type nodeRegistration struct {
	TokenRequired            bool    `json:"token_required"` // TM_NODE_TOKEN is set
	HeartbeatIntervalSeconds float64 `json:"heartbeat_interval_seconds"`
	OfflineAfterSeconds      float64 `json:"offline_after_seconds"`
	// LocalTrainerMode is TM_TRAINER_MODE of this machine's own training
	// node ("auto", "gpu", "cpu" or "off"); null when not known.
	LocalTrainerMode *string `json:"local_trainer_mode"`
}

func (s *server) getComputeServer(w http.ResponseWriter, _ *http.Request) {
	reg := nodeRegistration{
		TokenRequired:            s.nodeToken != "",
		HeartbeatIntervalSeconds: s.compute.HeartbeatInterval().Seconds(),
		OfflineAfterSeconds:      s.compute.OfflineAfter().Seconds(),
	}
	if s.localTrainerMode != "" {
		reg.LocalTrainerMode = &s.localTrainerMode
	}
	writeJSON(w, http.StatusOK, computeServerInfo{Server: s.host.Info(), NodeRegistration: reg})
}

func (s *server) listComputeNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.compute.List(r.Context())
	if err != nil {
		s.computeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(nodes, len(nodes)))
}

func (s *server) getComputeNode(w http.ResponseWriter, r *http.Request) {
	id, ok := computeNodeID(w, r)
	if !ok {
		return
	}
	n, err := s.compute.Get(r.Context(), id)
	if err != nil {
		s.computeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *server) deleteComputeNode(w http.ResponseWriter, r *http.Request) {
	id, ok := computeNodeID(w, r)
	if !ok {
		return
	}
	if err := s.compute.Delete(r.Context(), id); err != nil {
		s.computeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// probeComputeNode asks the node to probe again; it picks the request up
// with its next heartbeat, hence 202.
func (s *server) probeComputeNode(w http.ResponseWriter, r *http.Request) {
	id, ok := computeNodeID(w, r)
	if !ok {
		return
	}
	n, err := s.compute.RequestProbe(r.Context(), id)
	if err != nil {
		s.computeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, n)
}

func (s *server) registerComputeNode(w http.ResponseWriter, r *http.Request) {
	body, ok := readNodeRequest(w, r)
	if !ok {
		return
	}
	in, err := compute.DecodeRegister(body)
	if err != nil {
		badRequest(w, msgMalformedRequest)
		return
	}
	res, err := s.compute.Register(r.Context(), in, nodeAddress(r))
	if err != nil {
		s.computeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) computeNodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	body, ok := readNodeRequest(w, r)
	if !ok {
		return
	}
	in, err := compute.DecodeHeartbeat(body)
	if err != nil {
		badRequest(w, msgMalformedRequest)
		return
	}
	res, err := s.compute.Heartbeat(r.Context(), in, nodeAddress(r))
	if err != nil {
		s.computeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// readNodeRequest reads the body of a node agent request, which is
// decoded by package compute (more leniently than decodeJSON does). It
// writes the error response and returns false on failure.
func readNodeRequest(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, "请求内容过大")
		} else {
			badRequest(w, msgMalformedRequest)
		}
		return nil, false
	}
	return body, true
}

// nodeAddress is the client IP of a node request (after
// TM_TRUSTED_PROXIES), or "" if there is none.
func nodeAddress(r *http.Request) string {
	addr, err := netip.ParseAddr(clientIP(r))
	if err != nil {
		return ""
	}
	return addr.Unmap().WithZone("").String()
}

// requireNodeToken rejects node agent requests without the configured
// bearer token. It is a no-op when TM_NODE_TOKEN is not set.
func (s *server) requireNodeToken(next http.Handler) http.Handler {
	if s.nodeToken == "" {
		return next
	}
	want := []byte(s.nodeToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			// Agents back off between attempts, so a misconfigured node
			// logs about once a minute.
			s.log.Warn("compute node request rejected: missing or wrong node token",
				"path", r.URL.Path, "client_ip", clientIP(r), "request_id", RequestID(r.Context()))
			w.Header().Set("WWW-Authenticate", `Bearer realm="trainmaster"`)
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "节点令牌不正确")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the token of an "Authorization: Bearer <token>"
// header (the scheme is case-insensitive).
func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
