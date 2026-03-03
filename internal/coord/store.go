package coord

import (
	"database/sql"
	"fmt"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	_ "modernc.org/sqlite"
	"google.golang.org/protobuf/encoding/protojson"
)

// NodeRecord represents a registered node in the database.
type NodeRecord struct {
	NodeID          string
	PublicKey       []byte
	AdvertiseAddr   string
	Handles         []string
	HandleManifests map[string]*messagepb.ServiceManifest
	LastHeartbeat   time.Time
	IsRelay         bool
}

// Store provides SQLite persistence for the coordination server.
type Store struct {
	db *sql.DB
}

// NewStore opens or creates a SQLite database at the given path.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS nodes (
			node_id TEXT PRIMARY KEY,
			public_key BLOB NOT NULL,
			advertise_addr TEXT NOT NULL,
			last_heartbeat INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS handles (
			handle TEXT PRIMARY KEY,
			node_id TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
			description TEXT NOT NULL DEFAULT '',
			manifest TEXT NOT NULL DEFAULT '{}'
		);
	`)
	if err != nil {
		return err
	}
	// Additive migrations for existing databases.
	s.db.Exec("ALTER TABLE handles ADD COLUMN description TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE handles ADD COLUMN manifest TEXT NOT NULL DEFAULT '{}'")
	s.db.Exec("ALTER TABLE nodes ADD COLUMN is_relay INTEGER NOT NULL DEFAULT 0")

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS auth_tokens (
			name TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			single_use INTEGER NOT NULL DEFAULT 0,
			used_by TEXT,
			created_at INTEGER NOT NULL,
			expires_at INTEGER
		);
	`)
	if err != nil {
		return fmt.Errorf("create auth_tokens table: %w", err)
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			email TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL,
			last_login INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS node_users (
			node_id TEXT NOT NULL,
			email TEXT NOT NULL,
			bound_at INTEGER NOT NULL,
			PRIMARY KEY (node_id, email)
		);
	`)
	if err != nil {
		return fmt.Errorf("create users tables: %w", err)
	}

	return nil
}

func marshalManifest(m *messagepb.ServiceManifest) string {
	if m == nil {
		return "{}"
	}
	b, err := protojson.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func unmarshalManifest(data string) *messagepb.ServiceManifest {
	if data == "" || data == "{}" {
		return nil
	}
	m := &messagepb.ServiceManifest{}
	if err := protojson.Unmarshal([]byte(data), m); err != nil {
		return nil
	}
	return m
}

// UpsertNode inserts or updates a node record and its handles.
func (s *Store) UpsertNode(rec *NodeRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	isRelay := 0
	if rec.IsRelay {
		isRelay = 1
	}
	_, err = tx.Exec(`
		INSERT INTO nodes (node_id, public_key, advertise_addr, last_heartbeat, is_relay)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			public_key = excluded.public_key,
			advertise_addr = excluded.advertise_addr,
			last_heartbeat = excluded.last_heartbeat,
			is_relay = excluded.is_relay
	`, rec.NodeID, rec.PublicKey, rec.AdvertiseAddr, rec.LastHeartbeat.Unix(), isRelay)
	if err != nil {
		return err
	}

	// Remove old handles for this node
	_, err = tx.Exec("DELETE FROM handles WHERE node_id = ?", rec.NodeID)
	if err != nil {
		return err
	}

	// Insert new handles with manifests
	for _, h := range rec.Handles {
		var m *messagepb.ServiceManifest
		if rec.HandleManifests != nil {
			m = rec.HandleManifests[h]
		}
		desc := ""
		if m != nil {
			desc = m.Description
		}
		_, err = tx.Exec("INSERT INTO handles (handle, node_id, description, manifest) VALUES (?, ?, ?, ?)",
			h, rec.NodeID, desc, marshalManifest(m))
		if err != nil {
			return fmt.Errorf("register handle %q: %w", h, err)
		}
	}

	return tx.Commit()
}

// UpdateHeartbeat updates the heartbeat timestamp and handles for a node.
func (s *Store) UpdateHeartbeat(nodeID string, handles []string, manifests map[string]*messagepb.ServiceManifest) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec("UPDATE nodes SET last_heartbeat = ? WHERE node_id = ?", time.Now().Unix(), nodeID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("node %q not found", nodeID)
	}

	// Update handles
	_, err = tx.Exec("DELETE FROM handles WHERE node_id = ?", nodeID)
	if err != nil {
		return err
	}
	for _, h := range handles {
		var m *messagepb.ServiceManifest
		if manifests != nil {
			m = manifests[h]
		}
		desc := ""
		if m != nil {
			desc = m.Description
		}
		_, err = tx.Exec("INSERT OR REPLACE INTO handles (handle, node_id, description, manifest) VALUES (?, ?, ?, ?)",
			h, nodeID, desc, marshalManifest(m))
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetAllNodes returns all registered nodes with their handles.
func (s *Store) GetAllNodes() ([]*NodeRecord, error) {
	rows, err := s.db.Query("SELECT node_id, public_key, advertise_addr, last_heartbeat, is_relay FROM nodes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodeMap := make(map[string]*NodeRecord)
	var nodes []*NodeRecord
	for rows.Next() {
		var rec NodeRecord
		var ts int64
		var isRelay int
		if err := rows.Scan(&rec.NodeID, &rec.PublicKey, &rec.AdvertiseAddr, &ts, &isRelay); err != nil {
			return nil, err
		}
		rec.LastHeartbeat = time.Unix(ts, 0)
		rec.IsRelay = isRelay != 0
		nodeMap[rec.NodeID] = &rec
		nodes = append(nodes, &rec)
	}

	// Load handles with manifests
	hrows, err := s.db.Query("SELECT handle, node_id, description, manifest FROM handles")
	if err != nil {
		return nil, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var h, nodeID, desc, manifestJSON string
		if err := hrows.Scan(&h, &nodeID, &desc, &manifestJSON); err != nil {
			return nil, err
		}
		if rec, ok := nodeMap[nodeID]; ok {
			rec.Handles = append(rec.Handles, h)
			m := unmarshalManifest(manifestJSON)
			// Fall back to description for old rows that have no manifest
			if m == nil && desc != "" {
				m = &messagepb.ServiceManifest{Description: desc}
			}
			if m != nil {
				if rec.HandleManifests == nil {
					rec.HandleManifests = make(map[string]*messagepb.ServiceManifest)
				}
				rec.HandleManifests[h] = m
			}
		}
	}

	return nodes, nil
}

// LookupHandle finds which node serves a given handle.
func (s *Store) LookupHandle(handle string) (*NodeRecord, error) {
	var nodeID string
	err := s.db.QueryRow("SELECT node_id FROM handles WHERE handle = ?", handle).Scan(&nodeID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var rec NodeRecord
	var ts int64
	err = s.db.QueryRow("SELECT node_id, public_key, advertise_addr, last_heartbeat FROM nodes WHERE node_id = ?", nodeID).
		Scan(&rec.NodeID, &rec.PublicKey, &rec.AdvertiseAddr, &ts)
	if err != nil {
		return nil, err
	}
	rec.LastHeartbeat = time.Unix(ts, 0)

	hrows, err := s.db.Query("SELECT handle, manifest FROM handles WHERE node_id = ?", nodeID)
	if err != nil {
		return nil, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var h, manifestJSON string
		if err := hrows.Scan(&h, &manifestJSON); err != nil {
			return nil, err
		}
		rec.Handles = append(rec.Handles, h)
		m := unmarshalManifest(manifestJSON)
		if m != nil {
			if rec.HandleManifests == nil {
				rec.HandleManifests = make(map[string]*messagepb.ServiceManifest)
			}
			rec.HandleManifests[h] = m
		}
	}

	return &rec, nil
}

// RemoveNode removes a node and its handles.
func (s *Store) RemoveNode(nodeID string) error {
	_, err := s.db.Exec("DELETE FROM nodes WHERE node_id = ?", nodeID)
	return err
}

// RemoveStaleNodes removes nodes whose last heartbeat is older than the given time.
// Returns the number of nodes removed.
func (s *Store) RemoveStaleNodes(olderThan time.Time) (int, error) {
	res, err := s.db.Exec("DELETE FROM nodes WHERE last_heartbeat < ?", olderThan.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// InsertAuthToken inserts a new auth token record.
func (s *Store) InsertAuthToken(name, tokenHash string, singleUse bool, expiresAt *time.Time) error {
	su := 0
	if singleUse {
		su = 1
	}
	var exp *int64
	if expiresAt != nil {
		v := expiresAt.Unix()
		exp = &v
	}
	_, err := s.db.Exec(
		"INSERT INTO auth_tokens (name, token_hash, single_use, created_at, expires_at) VALUES (?, ?, ?, ?, ?)",
		name, tokenHash, su, time.Now().Unix(), exp,
	)
	return err
}

// ValidateAndConsumeToken checks that a token hash exists, is not expired,
// and has not been consumed (if single-use). If the token is single-use,
// it marks it as consumed by the given nodeID.
func (s *Store) ValidateAndConsumeToken(tokenHash, nodeID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var name string
	var singleUse int
	var usedBy sql.NullString
	var expiresAt sql.NullInt64
	err = tx.QueryRow(
		"SELECT name, single_use, used_by, expires_at FROM auth_tokens WHERE token_hash = ?",
		tokenHash,
	).Scan(&name, &singleUse, &usedBy, &expiresAt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("invalid auth token")
	}
	if err != nil {
		return err
	}

	if expiresAt.Valid && time.Now().Unix() > expiresAt.Int64 {
		return fmt.Errorf("auth token %q has expired", name)
	}

	if singleUse != 0 && usedBy.Valid {
		return fmt.Errorf("auth token %q has already been used", name)
	}

	if singleUse != 0 {
		_, err = tx.Exec("UPDATE auth_tokens SET used_by = ? WHERE token_hash = ?", nodeID, tokenHash)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// HasAuthTokens returns true if any auth tokens are configured.
func (s *Store) HasAuthTokens() (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM auth_tokens").Scan(&count)
	return count > 0, err
}

// RevokeAuthToken deletes an auth token by name.
func (s *Store) RevokeAuthToken(name string) error {
	_, err := s.db.Exec("DELETE FROM auth_tokens WHERE name = ?", name)
	return err
}

// UpsertUser creates or updates a user record, updating last_login.
func (s *Store) UpsertUser(email string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`
		INSERT INTO users (email, created_at, last_login) VALUES (?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET last_login = excluded.last_login
	`, email, now, now)
	return err
}

// BindNodeToUser associates a node with a user.
func (s *Store) BindNodeToUser(nodeID, email string) error {
	_, err := s.db.Exec(`
		INSERT INTO node_users (node_id, email, bound_at) VALUES (?, ?, ?)
		ON CONFLICT(node_id, email) DO UPDATE SET bound_at = excluded.bound_at
	`, nodeID, email, time.Now().Unix())
	return err
}

// GetNodeUser returns the email of the user bound to a node, or "" if none.
func (s *Store) GetNodeUser(nodeID string) (string, error) {
	var email string
	err := s.db.QueryRow("SELECT email FROM node_users WHERE node_id = ? LIMIT 1", nodeID).Scan(&email)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return email, err
}

// ListUserNodes returns all node IDs bound to a user.
func (s *Store) ListUserNodes(email string) ([]string, error) {
	rows, err := s.db.Query("SELECT node_id FROM node_users WHERE email = ?", email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, err
		}
		nodes = append(nodes, nodeID)
	}
	return nodes, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}
