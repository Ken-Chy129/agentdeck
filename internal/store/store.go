// Package store is the SQLite persistence layer.
//
// Everything the server distributes is a resource: a (kind, name) with
// versioned content and per-machine assignments. Kinds today: skill, env,
// config. Machines report back what they actually applied (machine_resources).
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS machines (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,
  token_hash    TEXT NOT NULL UNIQUE,
  os            TEXT NOT NULL DEFAULT '',
  arch          TEXT NOT NULL DEFAULT '',
  hostname      TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  last_seen_at  TEXT,
  inventory     TEXT,
  inventory_at  TEXT,
  local_skills  TEXT,
  snapshot      TEXT,
  snapshot_at   TEXT
);
CREATE TABLE IF NOT EXISTS enroll_tokens (
  token_hash TEXT PRIMARY KEY,
  note       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  used_at    TEXT
);
CREATE TABLE IF NOT EXISTS resources (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  kind               TEXT NOT NULL,
  name               TEXT NOT NULL,
  meta               TEXT NOT NULL DEFAULT '{}',
  current_version_id INTEGER,
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  UNIQUE(kind, name)
);
CREATE TABLE IF NOT EXISTS resource_versions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  resource_id INTEGER NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  version     INTEGER NOT NULL,
  digest      TEXT NOT NULL,
  size        INTEGER NOT NULL,
  content     BLOB NOT NULL,
  note        TEXT NOT NULL DEFAULT '',
  created_by  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  UNIQUE(resource_id, version)
);
CREATE TABLE IF NOT EXISTS assignments (
  machine_id  TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  resource_id INTEGER NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  override    TEXT,
  created_at  TEXT NOT NULL,
  PRIMARY KEY (machine_id, resource_id)
);
CREATE TABLE IF NOT EXISTS machine_resources (
  machine_id  TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  resource_id INTEGER NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  digest      TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT '',
  detail      TEXT NOT NULL DEFAULT '',
  updated_at  TEXT NOT NULL,
  PRIMARY KEY (machine_id, resource_id)
);
CREATE TABLE IF NOT EXISTS jobs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  machine_id  TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  type        TEXT NOT NULL,
  payload     TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'queued',
  result      TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  finished_at TEXT
);
CREATE TABLE IF NOT EXISTS sync_logs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  machine_id TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  at         TEXT NOT NULL,
  summary    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  at     TEXT NOT NULL,
  actor  TEXT NOT NULL,
  action TEXT NOT NULL,
  target TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sync_logs_machine ON sync_logs(machine_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_machine ON jobs(machine_id, status);
CREATE INDEX IF NOT EXISTS idx_resources_kind ON resources(kind, name);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	for _, col := range []string{"snapshot TEXT", "snapshot_at TEXT"} {
		_, _ = s.db.Exec("ALTER TABLE machines ADD COLUMN " + col)
	}
	return s.migrateLegacySkills()
}

// migrateLegacySkills moves rows from the v0.1 skill-only tables into resources.
func (s *Store) migrateLegacySkills() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='skills'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT name, description, current_version_id, created_at, updated_at FROM skills`)
	if err != nil {
		return err
	}
	type sk struct {
		name, desc, created, updated string
		cur                          sql.NullInt64
	}
	var skills []sk
	for rows.Next() {
		var k sk
		if err := rows.Scan(&k.name, &k.desc, &k.cur, &k.created, &k.updated); err != nil {
			rows.Close()
			return err
		}
		skills = append(skills, k)
	}
	rows.Close()
	for _, k := range skills {
		meta, _ := json.Marshal(map[string]string{"description": k.desc})
		res, err := tx.Exec(`INSERT INTO resources(kind,name,meta,created_at,updated_at) VALUES('skill',?,?,?,?)`, k.name, string(meta), k.created, k.updated)
		if err != nil {
			return err
		}
		rid, _ := res.LastInsertId()
		vrows, err := tx.Query(`SELECT id, version, digest, size, archive, note, created_by, created_at FROM skill_versions WHERE skill_name=? ORDER BY version`, k.name)
		if err != nil {
			return err
		}
		type ver struct {
			oldID, version, size      int64
			digest, note, by, created string
			archive                   []byte
		}
		var vers []ver
		for vrows.Next() {
			var v ver
			if err := vrows.Scan(&v.oldID, &v.version, &v.digest, &v.size, &v.archive, &v.note, &v.by, &v.created); err != nil {
				vrows.Close()
				return err
			}
			vers = append(vers, v)
		}
		vrows.Close()
		var curNew int64
		for _, v := range vers {
			r, err := tx.Exec(`INSERT INTO resource_versions(resource_id,version,digest,size,content,note,created_by,created_at) VALUES(?,?,?,?,?,?,?,?)`,
				rid, v.version, v.digest, v.size, v.archive, v.note, v.by, v.created)
			if err != nil {
				return err
			}
			newID, _ := r.LastInsertId()
			if k.cur.Valid && k.cur.Int64 == v.oldID {
				curNew = newID
			}
		}
		if curNew != 0 {
			if _, err := tx.Exec(`UPDATE resources SET current_version_id=? WHERE id=?`, curNew, rid); err != nil {
				return err
			}
		}
		// old assignments table had (machine_id, skill_name); it was replaced by CREATE IF NOT EXISTS only if absent,
		// so detect its shape.
		var hasSkillName int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('assignments') WHERE name='skill_name'`).Scan(&hasSkillName)
		if hasSkillName > 0 {
			arows, err := tx.Query(`SELECT machine_id, created_at FROM assignments WHERE skill_name=?`, k.name)
			if err != nil {
				return err
			}
			type as struct{ m, c string }
			var asg []as
			for arows.Next() {
				var a as
				arows.Scan(&a.m, &a.c)
				asg = append(asg, a)
			}
			arows.Close()
			if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS assignments_v2 (machine_id TEXT NOT NULL, resource_id INTEGER NOT NULL, override TEXT, created_at TEXT NOT NULL, PRIMARY KEY(machine_id,resource_id))`); err != nil {
				return err
			}
			for _, a := range asg {
				tx.Exec(`INSERT OR IGNORE INTO assignments_v2(machine_id,resource_id,created_at) VALUES(?,?,?)`, a.m, rid, a.c)
			}
		}
	}
	var hasSkillName int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('assignments') WHERE name='skill_name'`).Scan(&hasSkillName)
	if hasSkillName > 0 {
		if _, err := tx.Exec(`DROP TABLE assignments`); err != nil {
			return err
		}
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS assignments_v2 (machine_id TEXT NOT NULL, resource_id INTEGER NOT NULL, override TEXT, created_at TEXT NOT NULL, PRIMARY KEY(machine_id,resource_id))`); err != nil {
			return err
		}
		if _, err := tx.Exec(`ALTER TABLE assignments_v2 RENAME TO assignments`); err != nil {
			return err
		}
	}
	for _, t := range []string{"skill_versions", "skills"} {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func NewToken(prefix string) string {
	b := make([]byte, 24)
	rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func Digest(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// ---- machines ----

type Machine struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	OS          string          `json:"os"`
	Arch        string          `json:"arch"`
	Hostname    string          `json:"hostname"`
	CreatedAt   string          `json:"created_at"`
	LastSeenAt  string          `json:"last_seen_at,omitempty"`
	Inventory   json.RawMessage `json:"inventory,omitempty"`
	InventoryAt string          `json:"inventory_at,omitempty"`
	LocalSkills json.RawMessage `json:"local_skills,omitempty"`
	Snapshot    json.RawMessage `json:"snapshot,omitempty"`
	SnapshotAt  string          `json:"snapshot_at,omitempty"`
}

const machineCols = `id,name,os,arch,hostname,created_at,last_seen_at,inventory,inventory_at,local_skills,snapshot,snapshot_at`

func scanMachineRow(sc interface{ Scan(...any) error }) (*Machine, error) {
	m := &Machine{}
	var last, inv, invAt, local, snap, snapAt sql.NullString
	if err := sc.Scan(&m.ID, &m.Name, &m.OS, &m.Arch, &m.Hostname, &m.CreatedAt, &last, &inv, &invAt, &local, &snap, &snapAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.LastSeenAt, m.InventoryAt, m.SnapshotAt = last.String, invAt.String, snapAt.String
	if inv.Valid {
		m.Inventory = json.RawMessage(inv.String)
	}
	if local.Valid {
		m.LocalSkills = json.RawMessage(local.String)
	}
	if snap.Valid {
		m.Snapshot = json.RawMessage(snap.String)
	}
	return m, nil
}

func (s *Store) CreateEnrollToken(ctx context.Context, note string) (string, error) {
	tok := NewToken("adenroll")
	_, err := s.db.ExecContext(ctx, `INSERT INTO enroll_tokens(token_hash,note,created_at) VALUES(?,?,?)`, HashToken(tok), note, now())
	return tok, err
}

func (s *Store) Enroll(ctx context.Context, enrollToken, name, os, arch, hostname string) (*Machine, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE enroll_tokens SET used_at=? WHERE token_hash=? AND used_at IS NULL`, now(), HashToken(enrollToken))
	if err != nil {
		return nil, "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, "", errors.New("invalid or already used enroll token")
	}
	mtok := NewToken("adm")
	m := &Machine{ID: NewToken("m")[2:14], Name: name, OS: os, Arch: arch, Hostname: hostname, CreatedAt: now()}
	_, err = tx.ExecContext(ctx, `INSERT INTO machines(id,name,token_hash,os,arch,hostname,created_at) VALUES(?,?,?,?,?,?,?)`,
		m.ID, m.Name, HashToken(mtok), os, arch, hostname, m.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, "", fmt.Errorf("machine name %q already exists", name)
		}
		return nil, "", err
	}
	return m, mtok, tx.Commit()
}

func (s *Store) MachineByToken(ctx context.Context, tok string) (*Machine, error) {
	return scanMachineRow(s.db.QueryRowContext(ctx, `SELECT `+machineCols+` FROM machines WHERE token_hash=?`, HashToken(tok)))
}

func (s *Store) MachineByID(ctx context.Context, id string) (*Machine, error) {
	return scanMachineRow(s.db.QueryRowContext(ctx, `SELECT `+machineCols+` FROM machines WHERE id=?`, id))
}

func (s *Store) ListMachines(ctx context.Context) ([]*Machine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+machineCols+` FROM machines ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Machine{}
	for rows.Next() {
		m, err := scanMachineRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) TouchMachine(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE machines SET last_seen_at=? WHERE id=?`, now(), id)
	return err
}

func (s *Store) SaveInventory(ctx context.Context, id string, inv, localSkills json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE machines SET inventory=?, inventory_at=?, local_skills=?, last_seen_at=? WHERE id=?`,
		string(inv), now(), string(localSkills), now(), id)
	return err
}

func (s *Store) SaveSnapshot(ctx context.Context, id string, snap json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE machines SET snapshot=?, snapshot_at=? WHERE id=?`, string(snap), now(), id)
	return err
}

func (s *Store) DeleteMachine(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM machines WHERE id=?`, id)
	return err
}

func (s *Store) RenameMachine(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE machines SET name=? WHERE id=?`, name, id)
	return err
}

// ---- resources ----

type Resource struct {
	ID               int64           `json:"id"`
	Kind             string          `json:"kind"`
	Name             string          `json:"name"`
	Meta             json.RawMessage `json:"meta"`
	CurrentVersionID int64           `json:"current_version_id"`
	CurrentVersion   int             `json:"current_version"`
	CurrentDigest    string          `json:"current_digest"`
	CurrentSize      int64           `json:"current_size"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
	MachineCount     int             `json:"machine_count"`
}

type Version struct {
	ID         int64  `json:"id"`
	ResourceID int64  `json:"resource_id"`
	Version    int    `json:"version"`
	Digest     string `json:"digest"`
	Size       int64  `json:"size"`
	Note       string `json:"note"`
	CreatedBy  string `json:"created_by"`
	CreatedAt  string `json:"created_at"`
}

const resourceSelect = `SELECT r.id, r.kind, r.name, r.meta, COALESCE(r.current_version_id,0), COALESCE(v.version,0), COALESCE(v.digest,''), COALESCE(v.size,0), r.created_at, r.updated_at,
  (SELECT COUNT(*) FROM assignments a WHERE a.resource_id=r.id)
FROM resources r LEFT JOIN resource_versions v ON v.id=r.current_version_id `

func scanResource(sc interface{ Scan(...any) error }) (*Resource, error) {
	r := &Resource{}
	var meta string
	if err := sc.Scan(&r.ID, &r.Kind, &r.Name, &meta, &r.CurrentVersionID, &r.CurrentVersion, &r.CurrentDigest, &r.CurrentSize, &r.CreatedAt, &r.UpdatedAt, &r.MachineCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Meta = json.RawMessage(meta)
	return r, nil
}

func (s *Store) ListResources(ctx context.Context, kind string) ([]*Resource, error) {
	q, args := resourceSelect+`ORDER BY r.kind, r.name`, []any{}
	if kind != "" {
		q, args = resourceSelect+`WHERE r.kind=? ORDER BY r.name`, []any{kind}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Resource{}
	for rows.Next() {
		r, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetResource(ctx context.Context, id int64) (*Resource, error) {
	return scanResource(s.db.QueryRowContext(ctx, resourceSelect+`WHERE r.id=?`, id))
}

func (s *Store) GetResourceByName(ctx context.Context, kind, name string) (*Resource, error) {
	return scanResource(s.db.QueryRowContext(ctx, resourceSelect+`WHERE r.kind=? AND r.name=?`, kind, name))
}

// Publish upserts a resource and stores content as a new version unless the
// digest equals the current one. meta is merged shallowly when non-empty.
func (s *Store) Publish(ctx context.Context, kind, name string, meta map[string]any, digest string, content []byte, note, by string) (*Resource, *Version, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, false, err
	}
	defer tx.Rollback()
	ts := now()
	var rid int64
	var curMeta string
	err = tx.QueryRowContext(ctx, `SELECT id, meta FROM resources WHERE kind=? AND name=?`, kind, name).Scan(&rid, &curMeta)
	if errors.Is(err, sql.ErrNoRows) {
		mb, _ := json.Marshal(nonNil(meta))
		res, err := tx.ExecContext(ctx, `INSERT INTO resources(kind,name,meta,created_at,updated_at) VALUES(?,?,?,?,?)`, kind, name, string(mb), ts, ts)
		if err != nil {
			return nil, nil, false, err
		}
		rid, _ = res.LastInsertId()
	} else if err != nil {
		return nil, nil, false, err
	} else if len(meta) > 0 {
		merged := map[string]any{}
		_ = json.Unmarshal([]byte(curMeta), &merged)
		for k, v := range meta {
			if v == nil || v == "" {
				continue
			}
			merged[k] = v
		}
		mb, _ := json.Marshal(merged)
		if _, err := tx.ExecContext(ctx, `UPDATE resources SET meta=?, updated_at=? WHERE id=?`, string(mb), ts, rid); err != nil {
			return nil, nil, false, err
		}
	}
	var curID sql.NullInt64
	var curDigest sql.NullString
	var curVer sql.NullInt64
	_ = tx.QueryRowContext(ctx, `SELECT v.id, v.digest, v.version FROM resources r JOIN resource_versions v ON v.id=r.current_version_id WHERE r.id=?`, rid).Scan(&curID, &curDigest, &curVer)
	if content == nil {
		if err := tx.Commit(); err != nil {
			return nil, nil, false, err
		}
		r, err := s.GetResource(ctx, rid)
		return r, nil, false, err
	}
	if curDigest.Valid && curDigest.String == digest {
		if err := tx.Commit(); err != nil {
			return nil, nil, false, err
		}
		r, err := s.GetResource(ctx, rid)
		return r, &Version{ID: curID.Int64, ResourceID: rid, Version: int(curVer.Int64), Digest: digest}, false, err
	}
	var maxVer int
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM resource_versions WHERE resource_id=?`, rid).Scan(&maxVer)
	v := &Version{ResourceID: rid, Version: maxVer + 1, Digest: digest, Size: int64(len(content)), Note: note, CreatedBy: by, CreatedAt: ts}
	res, err := tx.ExecContext(ctx, `INSERT INTO resource_versions(resource_id,version,digest,size,content,note,created_by,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		rid, v.Version, digest, v.Size, content, note, by, ts)
	if err != nil {
		return nil, nil, false, err
	}
	v.ID, _ = res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `UPDATE resources SET current_version_id=?, updated_at=? WHERE id=?`, v.ID, ts, rid); err != nil {
		return nil, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, false, err
	}
	r, err := s.GetResource(ctx, rid)
	return r, v, true, err
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func (s *Store) UpdateMeta(ctx context.Context, id int64, meta map[string]any) error {
	var cur string
	if err := s.db.QueryRowContext(ctx, `SELECT meta FROM resources WHERE id=?`, id).Scan(&cur); err != nil {
		return err
	}
	merged := map[string]any{}
	_ = json.Unmarshal([]byte(cur), &merged)
	for k, v := range meta {
		merged[k] = v
	}
	mb, _ := json.Marshal(merged)
	_, err := s.db.ExecContext(ctx, `UPDATE resources SET meta=?, updated_at=? WHERE id=?`, string(mb), now(), id)
	return err
}

func (s *Store) RenameResource(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE resources SET name=?, updated_at=? WHERE id=?`, name, now(), id)
	return err
}

func (s *Store) DeleteResource(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM resources WHERE id=?`, id)
	return err
}

func (s *Store) ListVersions(ctx context.Context, rid int64) ([]*Version, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,resource_id,version,digest,size,note,created_by,created_at FROM resource_versions WHERE resource_id=? ORDER BY version DESC`, rid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Version{}
	for rows.Next() {
		v := &Version{}
		if err := rows.Scan(&v.ID, &v.ResourceID, &v.Version, &v.Digest, &v.Size, &v.Note, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetVersion(ctx context.Context, id int64) (*Version, []byte, error) {
	v := &Version{}
	var content []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,resource_id,version,digest,size,note,created_by,created_at,content FROM resource_versions WHERE id=?`, id).
		Scan(&v.ID, &v.ResourceID, &v.Version, &v.Digest, &v.Size, &v.Note, &v.CreatedBy, &v.CreatedAt, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	return v, content, err
}

// CurrentContent returns the current version's content for a resource.
func (s *Store) CurrentContent(ctx context.Context, rid int64) (*Version, []byte, error) {
	var vid sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT current_version_id FROM resources WHERE id=?`, rid).Scan(&vid); err != nil {
		return nil, nil, err
	}
	if !vid.Valid {
		return nil, nil, ErrNotFound
	}
	return s.GetVersion(ctx, vid.Int64)
}

func (s *Store) SetCurrentVersion(ctx context.Context, rid, vid int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE resources SET current_version_id=?, updated_at=? WHERE id=? AND EXISTS(SELECT 1 FROM resource_versions WHERE id=? AND resource_id=?)`, vid, now(), rid, vid, rid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- assignments ----

type Assignment struct {
	MachineID  string `json:"machine_id"`
	ResourceID int64  `json:"resource_id"`
	Override   string `json:"override,omitempty"`
	HasOverr   bool   `json:"has_override"`
	CreatedAt  string `json:"created_at"`
}

func (s *Store) Assign(ctx context.Context, machineID string, rid int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO assignments(machine_id,resource_id,created_at) VALUES(?,?,?)`, machineID, rid, now())
	return err
}

func (s *Store) SetOverride(ctx context.Context, machineID string, rid int64, override *string) error {
	if err := s.Assign(ctx, machineID, rid); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE assignments SET override=? WHERE machine_id=? AND resource_id=?`, override, machineID, rid)
	return err
}

func (s *Store) Unassign(ctx context.Context, machineID string, rid int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM assignments WHERE machine_id=? AND resource_id=?`, machineID, rid)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM machine_resources WHERE machine_id=? AND resource_id=?`, machineID, rid)
	return err
}

// AllAssignments returns every assignment (override values included, still sealed for secrets).
func (s *Store) AllAssignments(ctx context.Context) ([]*Assignment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT machine_id, resource_id, override, created_at FROM assignments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Assignment{}
	for rows.Next() {
		a := &Assignment{}
		var ov sql.NullString
		if err := rows.Scan(&a.MachineID, &a.ResourceID, &ov, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Override, a.HasOverr = ov.String, ov.Valid
		out = append(out, a)
	}
	return out, rows.Err()
}

// Desired is one resource as it should exist on one machine.
type Desired struct {
	Resource *Resource
	Version  *Version
	Content  []byte
	Override *string
}

func (s *Store) DesiredFor(ctx context.Context, machineID string) ([]Desired, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, a.override FROM assignments a JOIN resources r ON r.id=a.resource_id WHERE a.machine_id=? AND r.current_version_id IS NOT NULL ORDER BY r.kind, r.name`, machineID)
	if err != nil {
		return nil, err
	}
	type row struct {
		id int64
		ov sql.NullString
	}
	var ids []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.ov); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, r)
	}
	rows.Close()
	out := []Desired{}
	for _, r := range ids {
		res, err := s.GetResource(ctx, r.id)
		if err != nil {
			return nil, err
		}
		v, content, err := s.CurrentContent(ctx, r.id)
		if err != nil {
			continue
		}
		d := Desired{Resource: res, Version: v, Content: content}
		if r.ov.Valid {
			ov := r.ov.String
			d.Override = &ov
		}
		out = append(out, d)
	}
	return out, nil
}

// ---- machine_resources (reported state) ----

type MachineResource struct {
	MachineID  string `json:"machine_id"`
	ResourceID int64  `json:"resource_id"`
	Digest     string `json:"digest"`
	Status     string `json:"status"`
	Detail     string `json:"detail"`
	UpdatedAt  string `json:"updated_at"`
}

func (s *Store) UpsertMachineResource(ctx context.Context, m MachineResource) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO machine_resources(machine_id,resource_id,digest,status,detail,updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(machine_id,resource_id) DO UPDATE SET digest=excluded.digest, status=excluded.status, detail=excluded.detail, updated_at=excluded.updated_at`,
		m.MachineID, m.ResourceID, m.Digest, m.Status, m.Detail, now())
	return err
}

func (s *Store) AllMachineResources(ctx context.Context) ([]*MachineResource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT machine_id,resource_id,digest,status,detail,updated_at FROM machine_resources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MachineResource{}
	for rows.Next() {
		m := &MachineResource{}
		if err := rows.Scan(&m.MachineID, &m.ResourceID, &m.Digest, &m.Status, &m.Detail, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- jobs ----

type Job struct {
	ID         int64           `json:"id"`
	MachineID  string          `json:"machine_id"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	Status     string          `json:"status"`
	Result     string          `json:"result"`
	CreatedAt  string          `json:"created_at"`
	FinishedAt string          `json:"finished_at,omitempty"`
}

func (s *Store) CreateJob(ctx context.Context, machineID, typ string, payload any) (*Job, error) {
	b, _ := json.Marshal(payload)
	ts := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO jobs(machine_id,type,payload,created_at) VALUES(?,?,?,?)`, machineID, typ, string(b), ts)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Job{ID: id, MachineID: machineID, Type: typ, Payload: b, Status: "queued", CreatedAt: ts}, nil
}

func (s *Store) ListJobs(ctx context.Context, machineID string, limit int) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,machine_id,type,payload,status,result,created_at,finished_at FROM jobs WHERE machine_id=? ORDER BY id DESC LIMIT ?`, machineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

func (s *Store) QueuedJobs(ctx context.Context, machineID string) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,machine_id,type,payload,status,result,created_at,finished_at FROM jobs WHERE machine_id=? AND status='queued' ORDER BY id`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

// ClaimJobs hands queued jobs to exactly one caller by flipping them to
// 'running' in the same transaction. Both the long poll and the scheduled sync
// pull from this queue, and without claiming they'd run a command twice.
func (s *Store) ClaimJobs(ctx context.Context, machineID string) ([]*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,machine_id,type,payload,status,result,created_at,finished_at FROM jobs WHERE machine_id=? AND status='queued' ORDER BY id`, machineID)
	if err != nil {
		return nil, err
	}
	jobs, err := scanJobs(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status='running' WHERE id=?`, j.ID); err != nil {
			return nil, err
		}
		j.Status = "running"
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// RequeueStaleJobs rescues jobs whose machine died mid-run, so they don't sit
// in 'running' forever.
func (s *Store) RequeueStaleJobs(ctx context.Context, olderThan time.Duration) error {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='failed', result='machine went away while running', finished_at=? WHERE status='running' AND created_at < ?`, now(), cutoff)
	return err
}

// JobByID fetches a single job so the console can poll one command's result.
func (s *Store) JobByID(ctx context.Context, id int64) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,machine_id,type,payload,status,result,created_at,finished_at FROM jobs WHERE id=?`, id)
	jobs, err := scanJobRow(row)
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

func scanJobRow(row *sql.Row) (*Job, error) {
	j := &Job{}
	var payload string
	var fin sql.NullString
	if err := row.Scan(&j.ID, &j.MachineID, &j.Type, &payload, &j.Status, &j.Result, &j.CreatedAt, &fin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	j.Payload = json.RawMessage(payload)
	j.FinishedAt = fin.String
	return j, nil
}

func scanJobs(rows *sql.Rows) ([]*Job, error) {
	out := []*Job{}
	for rows.Next() {
		j := &Job{}
		var payload string
		var fin sql.NullString
		if err := rows.Scan(&j.ID, &j.MachineID, &j.Type, &payload, &j.Status, &j.Result, &j.CreatedAt, &fin); err != nil {
			return nil, err
		}
		j.Payload = json.RawMessage(payload)
		j.FinishedAt = fin.String
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) FinishJob(ctx context.Context, machineID string, id int64, status, result string) error {
	if len(result) > 64*1024 {
		result = result[:64*1024] + "\n…(truncated)"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status=?, result=?, finished_at=? WHERE id=? AND machine_id=?`, status, result, now(), id, machineID)
	return err
}

func (s *Store) CancelJob(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='cancelled', finished_at=? WHERE id=? AND status='queued'`, now(), id)
	return err
}

// ---- sync logs ----

type SyncLog struct {
	ID        int64           `json:"id"`
	MachineID string          `json:"machine_id"`
	At        string          `json:"at"`
	Summary   json.RawMessage `json:"summary"`
}

func (s *Store) AddSyncLog(ctx context.Context, machineID string, summary json.RawMessage) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sync_logs(machine_id,at,summary) VALUES(?,?,?)`, machineID, now(), string(summary)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sync_logs WHERE machine_id=? AND id NOT IN (SELECT id FROM sync_logs WHERE machine_id=? ORDER BY id DESC LIMIT 200)`, machineID, machineID)
	return err
}

func (s *Store) ListSyncLogs(ctx context.Context, machineID string, limit int) ([]*SyncLog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,machine_id,at,summary FROM sync_logs WHERE machine_id=? ORDER BY id DESC LIMIT ?`, machineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SyncLog{}
	for rows.Next() {
		l := &SyncLog{}
		var sum string
		if err := rows.Scan(&l.ID, &l.MachineID, &l.At, &sum); err != nil {
			return nil, err
		}
		l.Summary = json.RawMessage(sum)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---- audit ----

type Audit struct {
	ID     int64  `json:"id"`
	At     string `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail"`
}

func (s *Store) Audit(ctx context.Context, actor, action, target, detail string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO audit(at,actor,action,target,detail) VALUES(?,?,?,?,?)`, now(), actor, action, target, detail)
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]*Audit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,at,actor,action,target,detail FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Audit{}
	for rows.Next() {
		a := &Audit{}
		if err := rows.Scan(&a.ID, &a.At, &a.Actor, &a.Action, &a.Target, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
