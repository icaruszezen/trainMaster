package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// querier is implemented by *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// nodeColumns are the columns scanRow reads.
const nodeColumns = `id, node_key, name, hostname, address, agent_version, hardware, environment, metrics,
	probe_requested_at, registered_at, last_seen_at, created_at`

// nodeRow is a row of compute_nodes with its report sections still JSON.
type nodeRow struct {
	ID               int64
	NodeKey          string
	Name             string
	Hostname         string
	Address          string
	AgentVersion     string
	Hardware         []byte
	Environment      []byte // nil: NULL
	Metrics          []byte
	ProbeRequestedAt *time.Time
	RegisteredAt     time.Time
	LastSeenAt       time.Time
	CreatedAt        time.Time
}

func scanRow(row pgx.Row) (nodeRow, error) {
	var n nodeRow
	err := row.Scan(&n.ID, &n.NodeKey, &n.Name, &n.Hostname, &n.Address, &n.AgentVersion,
		&n.Hardware, &n.Environment, &n.Metrics, &n.ProbeRequestedAt, &n.RegisteredAt, &n.LastSeenAt, &n.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, errNodeNotFound
	}
	return n, err
}

func queryNode(ctx context.Context, q querier, id int64) (nodeRow, error) {
	return scanRow(q.QueryRow(ctx, "SELECT "+nodeColumns+" FROM compute_nodes WHERE id = $1", id))
}

func queryNodes(ctx context.Context, q querier) ([]nodeRow, error) {
	rows, err := q.Query(ctx, "SELECT "+nodeColumns+" FROM compute_nodes")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (nodeRow, error) { return scanRow(row) })
}

// nodeIDByKey returns the ID of the node with nodeKey; errUnknownNode if
// there is none.
func nodeIDByKey(ctx context.Context, q querier, nodeKey string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, "SELECT id FROM compute_nodes WHERE node_key = $1", nodeKey).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errUnknownNode
	}
	return id, err
}

func nodeExists(ctx context.Context, q querier, id int64) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM compute_nodes WHERE id = $1)", id).Scan(&exists)
	return exists, err
}

// sectionJSON encodes a normalized report section for a jsonb column; nil
// becomes SQL NULL.
func sectionJSON[T any](v *T) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode %T: %w", *v, err)
	}
	return data, nil
}

// decodeStored decodes a jsonb report section; NULL is nil.
func decodeStored[T any](data []byte) (*T, error) {
	if data == nil || string(data) == "null" {
		return nil, nil
	}
	var v T
	if err := decodeLenient(data, &v); err != nil {
		return nil, fmt.Errorf("decode stored %T: %w", v, err)
	}
	return &v, nil
}

// priorNode is what a registration of a known node_key found in its row.
type priorNode struct {
	Hostname   string
	Address    string
	LastSeenAt time.Time
}

// upsertNode stores a registration and returns the node ID and, for a
// known node_key, what its row held before (nil for a new node). A known
// node_key updates its row: environment and metrics only when given. A
// registration without an environment (the node started and is checking
// it) marks a probe as requested until a heartbeat brings the result; one
// with an environment leaves probe_requested_at as it is.
func upsertNode(ctx context.Context, q querier, in RegisterInput, address string, hardware, environment, metrics []byte,
	now time.Time) (int64, *priorNode, error) {
	var (
		id                   int64
		priorHost, priorAddr *string
		priorSeen            *time.Time
	)
	// The CTE reads the row as it was before the statement.
	err := q.QueryRow(ctx, `WITH prior AS (SELECT hostname, address, last_seen_at FROM compute_nodes WHERE node_key = $1)
		INSERT INTO compute_nodes AS n (node_key, name, hostname, address, agent_version,
			hardware, environment, metrics, probe_requested_at, registered_at, last_seen_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CASE WHEN $7::jsonb IS NULL THEN $9::timestamptz END, $9, $9, $9, $9)
		ON CONFLICT (node_key) DO UPDATE SET
			name = EXCLUDED.name, hostname = EXCLUDED.hostname, address = EXCLUDED.address,
			agent_version = EXCLUDED.agent_version, hardware = EXCLUDED.hardware,
			environment = coalesce(EXCLUDED.environment, n.environment),
			metrics = coalesce(EXCLUDED.metrics, n.metrics),
			probe_requested_at = coalesce(EXCLUDED.probe_requested_at, n.probe_requested_at),
			registered_at = EXCLUDED.registered_at, last_seen_at = EXCLUDED.last_seen_at,
			updated_at = EXCLUDED.updated_at
		RETURNING id, (SELECT hostname FROM prior), (SELECT address FROM prior), (SELECT last_seen_at FROM prior)`,
		in.NodeKey, in.Name, in.Hostname, address, in.AgentVersion, hardware, environment, metrics, now,
	).Scan(&id, &priorHost, &priorAddr, &priorSeen)
	if err != nil || priorSeen == nil {
		return id, nil, err
	}
	return id, &priorNode{Hostname: *priorHost, Address: *priorAddr, LastSeenAt: priorSeen.UTC()}, nil
}

// heartbeatNode records a heartbeat of a registered node: sections that
// are nil stay unchanged, and an environment completes a pending probe.
// It returns the node ID and whether a probe is (still) requested.
func heartbeatNode(ctx context.Context, q querier, nodeKey, address string, hardware, environment, metrics []byte,
	now time.Time) (id int64, probeRequested bool, err error) {
	err = q.QueryRow(ctx, `UPDATE compute_nodes SET
			last_seen_at = $2, updated_at = $2, address = $3,
			hardware = coalesce($4::jsonb, hardware),
			environment = coalesce($5::jsonb, environment),
			metrics = coalesce($6::jsonb, metrics),
			probe_requested_at = CASE WHEN $5::jsonb IS NULL THEN probe_requested_at END
		WHERE node_key = $1
		RETURNING id, probe_requested_at IS NOT NULL`,
		nodeKey, now, address, hardware, environment, metrics).Scan(&id, &probeRequested)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, errUnknownNode
	}
	return id, probeRequested, err
}

// deleteOfflineNode deletes a node that was last seen before cutoff. It
// reports whether it did; the condition is checked on the locked row, so
// a heartbeat that commits first keeps the node.
func deleteOfflineNode(ctx context.Context, q querier, id int64, cutoff time.Time) (bool, error) {
	tag, err := q.Exec(ctx, "DELETE FROM compute_nodes WHERE id = $1 AND last_seen_at < $2", id, cutoff)
	return tag.RowsAffected() == 1, err
}

// requestProbe marks a probe as requested on a node last seen at or after
// cutoff and returns the updated row; errNodeNotFound if there is no such
// node.
func requestProbe(ctx context.Context, q querier, id int64, cutoff, now time.Time) (nodeRow, error) {
	return scanRow(q.QueryRow(ctx, `UPDATE compute_nodes SET probe_requested_at = $3, updated_at = $3
		WHERE id = $1 AND last_seen_at >= $2
		RETURNING `+nodeColumns, id, cutoff, now))
}
