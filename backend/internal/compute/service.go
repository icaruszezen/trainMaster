// Package compute implements the registry of training nodes: trainer
// services on GPU (or CPU) machines that register with the platform, send
// heartbeats with live metrics and report their CUDA / PyTorch / YOLO
// environment. The latest reports are stored in PostgreSQL; whether a node
// is online is derived from when it was last seen, and a short metrics
// history is kept in memory.
package compute

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultHeartbeatInterval is the heartbeat interval when none is set
// (TM_NODE_HEARTBEAT_INTERVAL).
const DefaultHeartbeatInterval = 10 * time.Second

// offlineGrace is added to three heartbeat intervals before a node counts
// as offline, so a slow request or a short network hiccup does not flip
// it.
const offlineGrace = 5 * time.Second

// Options are the dependencies of a Service.
type Options struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	// HeartbeatInterval is how often nodes are told to send heartbeats;
	// zero means DefaultHeartbeatInterval.
	HeartbeatInterval time.Duration
	// Now returns the current time; nil means time.Now. Tests use it to
	// move nodes online and offline.
	Now func() time.Time
}

// Service implements the compute node operations used by the HTTP
// handlers.
type Service struct {
	db       *pgxpool.Pool
	log      *slog.Logger
	interval time.Duration
	clock    func() time.Time
	history  *History
}

// NewService creates a Service.
func NewService(o Options) *Service {
	s := &Service{db: o.DB, log: o.Logger, interval: o.HeartbeatInterval, clock: o.Now}
	if s.interval <= 0 {
		s.interval = DefaultHeartbeatInterval
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	// A node that was offline in between starts a new curve.
	s.history = NewHistory(s.OfflineAfter())
	return s
}

// HeartbeatInterval is how often nodes send heartbeats.
func (s *Service) HeartbeatInterval() time.Duration { return s.interval }

// OfflineAfter is how long after its last heartbeat a node counts as
// offline: three missed heartbeats plus a grace period.
func (s *Service) OfflineAfter() time.Duration { return OfflineAfter(s.interval) }

// OfflineAfter returns the offline threshold for a heartbeat interval.
func OfflineAfter(interval time.Duration) time.Duration { return 3*interval + offlineGrace }

// isOnline reports whether a node last seen at lastSeen is online at now.
func isOnline(now, lastSeen time.Time, offlineAfter time.Duration) bool {
	return now.Sub(lastSeen) <= offlineAfter
}

// now is the current time as PostgreSQL stores it (UTC, microseconds), so
// a time written and read back compares equal to the one computed here.
func (s *Service) now() time.Time {
	return s.clock().UTC().Truncate(time.Microsecond)
}

// Register records a node registration from address (the client IP) and
// tells the node how often to send heartbeats.
func (s *Service) Register(ctx context.Context, in RegisterInput, address string) (RegisterResult, error) {
	if err := validateNodeKey(in.NodeKey); err != nil {
		return RegisterResult{}, err
	}
	var err error
	if in.Name, err = normalizeNodeName(in.Name); err != nil {
		return RegisterResult{}, err
	}
	if in.Hostname, err = normalizeLabel(in.Hostname, "主机名", maxHostnameRunes); err != nil {
		return RegisterResult{}, err
	}
	if in.AgentVersion, err = normalizeLabel(in.AgentVersion, "节点版本号", maxAgentVersionRunes); err != nil {
		return RegisterResult{}, err
	}
	if in.Hardware == nil {
		return RegisterResult{}, errHardwareMissing
	}
	now := s.now()
	hardware := normalizeHardware(*in.Hardware)
	metrics := normalizeMetrics(in.Metrics)
	hw, env, m, err := encodeSections(&hardware, normalizeEnvironment(in.Environment, now), metrics)
	if err != nil {
		return RegisterResult{}, err
	}
	id, prior, err := upsertNode(ctx, s.db, in, address, hw, env, m, now)
	if err != nil {
		return RegisterResult{}, fmt.Errorf("register compute node: %w", err)
	}
	if metrics != nil {
		s.history.Append(id, historyPoint(now, metrics))
	}
	s.log.Info("compute node registered", "node_id", id, "name", in.Name, "address", address,
		"agent_version", in.AgentVersion, "gpus", len(hardware.GPUs))
	if s.sharedNodeKey(prior, in.Hostname, now) {
		s.log.Warn("compute node registered from another host while it was online: two machines may share one node key",
			"node_id", id, "name", in.Name, "hostname", in.Hostname, "address", address,
			"previous_hostname", prior.Hostname, "previous_address", prior.Address,
			"hint", "两台机器使用了同一个节点标识，页面上只会显示为一个节点。删除其中一台的 <状态目录>/node-key"+
				"（Docker 部署默认为 data/trainer/node-key，直接安装默认为 /var/lib/trainmaster/node-key）并重启它的训练节点服务，"+
				"它会作为新节点注册；只是修改了主机名时可以忽略")
	}
	return RegisterResult{NodeID: id, HeartbeatIntervalSeconds: s.interval.Seconds(), ServerTime: now}, nil
}

// sharedNodeKey reports whether a registration from hostname looks like a
// second machine using the node_key of prior: the node was still online
// (another agent sent a heartbeat moments ago) and reported another
// hostname. This happens when data/trainer is copied to another machine
// or a VM is cloned, which clones the node identity. Compose sets the
// trainer container's hostname to the machine's, so recreating the
// container is not suspected; neither is a node without a hostname.
func (s *Service) sharedNodeKey(prior *priorNode, hostname string, now time.Time) bool {
	return prior != nil && isOnline(now, prior.LastSeenAt, s.OfflineAfter()) &&
		prior.Hostname != "" && hostname != "" && !strings.EqualFold(prior.Hostname, hostname)
}

// Heartbeat records a heartbeat from address. A node_key that is not
// registered (any more) is ErrUnknownNode: the node should register again.
func (s *Service) Heartbeat(ctx context.Context, in HeartbeatInput, address string) (HeartbeatResult, error) {
	if err := validateNodeKey(in.NodeKey); err != nil {
		return HeartbeatResult{}, err
	}
	now := s.now()
	var hardware *Hardware
	if in.Hardware != nil {
		h := normalizeHardware(*in.Hardware)
		hardware = &h
	}
	metrics := normalizeMetrics(in.Metrics)
	hw, env, m, err := encodeSections(hardware, normalizeEnvironment(in.Environment, now), metrics)
	if err != nil {
		return HeartbeatResult{}, err
	}
	id, probeRequested, err := heartbeatNode(ctx, s.db, in.NodeKey, address, hw, env, m, now)
	if errors.Is(err, errUnknownNode) {
		s.log.Info("heartbeat from an unregistered compute node", "node_key", in.NodeKey, "address", address)
		return HeartbeatResult{}, err
	}
	if err != nil {
		return HeartbeatResult{}, fmt.Errorf("compute node heartbeat: %w", err)
	}
	if metrics != nil {
		s.history.Append(id, historyPoint(now, metrics))
	}
	if env != nil {
		s.log.Debug("compute node reported its environment", "node_id", id)
	}
	return HeartbeatResult{NodeID: id, HeartbeatIntervalSeconds: s.interval.Seconds(), ProbeRequested: probeRequested}, nil
}

func encodeSections(h *Hardware, e *Environment, m *Metrics) (hw, env, metrics []byte, err error) {
	if hw, err = sectionJSON(h); err != nil {
		return nil, nil, nil, err
	}
	if env, err = sectionJSON(e); err != nil {
		return nil, nil, nil, err
	}
	if metrics, err = sectionJSON(m); err != nil {
		return nil, nil, nil, err
	}
	return hw, env, metrics, nil
}

// NodeID returns the ID of the node registered with nodeKey, for the task
// calls of the node agent (which identify the node by its key). A node_key
// that is not registered (any more) is ErrUnknownNode: the node should
// register again.
func (s *Service) NodeID(ctx context.Context, nodeKey string) (int64, error) {
	if err := validateNodeKey(nodeKey); err != nil {
		return 0, err
	}
	id, err := nodeIDByKey(ctx, s.db, nodeKey)
	if err != nil && !errors.Is(err, errUnknownNode) {
		return 0, fmt.Errorf("look up compute node: %w", err)
	}
	return id, err
}

// List returns all nodes: online ones first, then by name (ignoring case)
// and ID.
func (s *Service) List(ctx context.Context) ([]Node, error) {
	rows, err := queryNodes(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("list compute nodes: %w", err)
	}
	now := s.now()
	nodes := make([]Node, 0, len(rows))
	for _, row := range rows {
		n, err := s.toNode(row, now)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	slices.SortFunc(nodes, compareNodes)
	return nodes, nil
}

// compareNodes orders online nodes first, then by name ignoring case, then
// by ID.
func compareNodes(a, b Node) int {
	if aOn, bOn := a.Status == StatusOnline, b.Status == StatusOnline; aOn != bOn {
		if aOn {
			return -1
		}
		return 1
	}
	if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

// Get returns one node.
func (s *Service) Get(ctx context.Context, id int64) (Node, error) {
	row, err := queryNode(ctx, s.db, id)
	if err != nil {
		return Node{}, err
	}
	return s.toNode(row, s.now())
}

// Delete removes an offline node and its history. An online node cannot
// be deleted: its next heartbeat would only get "unknown node" and
// register it again.
func (s *Service) Delete(ctx context.Context, id int64) error {
	deleted, err := deleteOfflineNode(ctx, s.db, id, s.now().Add(-s.OfflineAfter()))
	if err != nil {
		return fmt.Errorf("delete compute node: %w", err)
	}
	if !deleted {
		return s.notOfflineError(ctx, id, errNodeOnline)
	}
	s.history.Remove(id)
	s.log.Info("compute node deleted", "node_id", id)
	return nil
}

// RequestProbe asks an online node to probe its hardware and environment
// again; it learns this from the answer to its next heartbeat. It returns
// the node with ProbePending set.
func (s *Service) RequestProbe(ctx context.Context, id int64) (Node, error) {
	now := s.now()
	row, err := requestProbe(ctx, s.db, id, now.Add(-s.OfflineAfter()), now)
	if errors.Is(err, errNodeNotFound) {
		return Node{}, s.notOfflineError(ctx, id, errNodeOffline)
	}
	if err != nil {
		return Node{}, fmt.Errorf("request compute node probe: %w", err)
	}
	s.log.Info("compute node probe requested", "node_id", id)
	return s.toNode(row, now)
}

// notOfflineError returns conflict if the node exists (so it failed an
// online or offline condition), else errNodeNotFound.
func (s *Service) notOfflineError(ctx context.Context, id int64, conflict error) error {
	exists, err := nodeExists(ctx, s.db, id)
	switch {
	case err != nil:
		return fmt.Errorf("look up compute node: %w", err)
	case exists:
		return conflict
	}
	return errNodeNotFound
}

// toNode turns a row into the API form at time now.
func (s *Service) toNode(row nodeRow, now time.Time) (Node, error) {
	n := Node{
		ID:           row.ID,
		NodeKey:      row.NodeKey,
		Name:         row.Name,
		Hostname:     row.Hostname,
		Address:      row.Address,
		AgentVersion: row.AgentVersion,
		Status:       StatusOffline,
		ProbePending: row.ProbeRequestedAt != nil,
		RegisteredAt: row.RegisteredAt.UTC(),
		LastSeenAt:   row.LastSeenAt.UTC(),
		CreatedAt:    row.CreatedAt.UTC(),
		History:      s.history.Points(row.ID),
	}
	if isOnline(now, n.LastSeenAt, s.OfflineAfter()) {
		n.Status = StatusOnline
	}
	// The sections were normalized when stored; normalizing again (it is
	// idempotent) keeps the contract for rows written by older versions.
	hardware, err := decodeStored[Hardware](row.Hardware)
	if err != nil {
		return Node{}, err
	}
	if hardware != nil {
		n.Hardware = normalizeHardware(*hardware)
	} else {
		n.Hardware = normalizeHardware(Hardware{})
	}
	env, err := decodeStored[Environment](row.Environment)
	if err != nil {
		return Node{}, err
	}
	n.Environment = normalizeEnvironment(env, n.LastSeenAt)
	metrics, err := decodeStored[Metrics](row.Metrics)
	if err != nil {
		return Node{}, err
	}
	n.Metrics = normalizeMetrics(metrics)
	return n, nil
}
