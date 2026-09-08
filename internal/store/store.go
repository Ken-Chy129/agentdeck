// Package store is the SQLite persistence layer.
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
CREATE TABLE IF NOT EXISTS skills (
  name               TEXT PRIMARY KEY,
  description        TEXT NOT NULL DEFAULT '',
  current_version_id INTEGER,
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS skill_versions (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  skill_name TEXT NOT NULL REFERENCES skills(name) ON DELETE CASCADE,
  version    INTEGER NOT NULL,
  digest     TEXT NOT NULL,
  size       INTEGER NOT NULL,
  archive    BLOB NOT NULL,
  note       TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(skill_name, version)
);
CREATE TABLE IF NOT EXISTS assignments (
  machine_id TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  skill_name TEXT NOT NULL REFERENCES skills(name) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  PRIMARY KEY (machine_id, skill_name)
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
CREATE INDEX IF NOT EXISTS idx_sync_logs_machine ON sync_logs(machine_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_machine ON jobs(machine_id, status);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// additive column migrations for existing databases
	for _, col := range []string{"snapshot TEXT", "snapshot_at TEXT"} {
		_, _ = s.db.Exec("ALTER TABLE machines ADD COLUMN " + col)
	}
	return nil
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

func (s *Store) CreateEnrollToken(ctx context.Context, note string) (string, error) {
	tok := NewToken("adenroll")
	_, err := s.db.ExecContext(ctx, `INSERT INTO enroll_tokens(token_hash,note,created_at) VALUES(?,?,?)`, HashToken(tok), note, now())
	return tok, err
}

// Enroll consumes an enroll token and creates a machine, returning its permanent token.
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
	return s.scanMachine(s.db.QueryRowContext(ctx, `SELECT `+machineCols+` FROM machines WHERE token_hash=?`, HashToken(tok)))
}

func (s *Store) MachineByID(ctx context.Context, id string) (*Machine, error) {
	return s.scanMachine(s.db.QueryRowContext(ctx, `SELECT `+machineCols+` FROM machines WHERE id=?`, id))
}

const machineCols = `id,name,os,arch,hostname,created_at,last_seen_at,inventory,inventory_at,local_skills,snapshot,snapshot_at`

func (s *Store) scanMachine(r *sql.Row) (*Machine, error) {
	m := &Machine{}
	var last, inv, invAt, local, snap, snapAt sql.NullString
	if err := r.Scan(&m.ID, &m.Name, &m.OS, &m.Arch, &m.Hostname, &m.CreatedAt, &last, &inv, &invAt, &local, &snap, &snapAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	fillMachine(m, last, inv, invAt, local, snap, snapAt)
	return m, nil
}

func (s *Store) ListMachines(ctx context.Context) ([]*Machine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+machineCols+` FROM machines ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Machine
	for rows.Next() {
		m := &Machine{}
		var last, inv, invAt, local, snap, snapAt sql.NullString
		if err := rows.Scan(&m.ID, &m.Name, &m.OS, &m.Arch, &m.Hostname, &m.CreatedAt, &last, &inv, &invAt, &local, &snap, &snapAt); err != nil {
			return nil, err
		}
		fillMachine(m, last, inv, invAt, local, snap, snapAt)
		out = append(out, m)
	}
	return out, rows.Err()
}

func fillMachine(m *Machine, last, inv, invAt, local, snap, snapAt sql.NullString) {
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
}

func (s *Store) SaveSnapshot(ctx context.Context, id string, snap json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE machines SET snapshot=?, snapshot_at=? WHERE id=?`, string(snap), now(), id)
	return err
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

func (s *Store) DeleteMachine(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM machines WHERE id=?`, id)
	return err
}

func (s *Store) RenameMachine(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE machines SET name=? WHERE id=?`, name, id)
	return err
}

// ---- skills ----

type Skill struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	CurrentVersionID int64  `json:"current_version_id"`
	CurrentVersion   int    `json:"current_version"`
	CurrentDigest    string `json:"current_digest"`
	CurrentSize      int64  `json:"current_size"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	MachineCount     int    `json:"machine_count"`
}

type SkillVersion struct {
	ID        int64  `json:"id"`
	SkillName string `json:"skill_name"`
	Version   int    `json:"version"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Note      string `json:"note"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
}

const skillSelect = `SELECT s.name, s.description, COALESCE(s.current_version_id,0), COALESCE(v.version,0), COALESCE(v.digest,''), COALESCE(v.size,0), s.created_at, s.updated_at,
  (SELECT COUNT(*) FROM assignments a WHERE a.skill_name=s.name)
FROM skills s LEFT JOIN skill_versions v ON v.id=s.current_version_id `

func (s *Store) ListSkills(ctx context.Context) ([]*Skill, error) {
	rows, err := s.db.QueryContext(ctx, skillSelect+`ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Skill
	for rows.Next() {
		k := &Skill{}
		if err := rows.Scan(&k.Name, &k.Description, &k.CurrentVersionID, &k.CurrentVersion, &k.CurrentDigest, &k.CurrentSize, &k.CreatedAt, &k.UpdatedAt, &k.MachineCount); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) GetSkill(ctx context.Context, name string) (*Skill, error) {
	k := &Skill{}
	err := s.db.QueryRowContext(ctx, skillSelect+`WHERE s.name=?`, name).
		Scan(&k.Name, &k.Description, &k.CurrentVersionID, &k.CurrentVersion, &k.CurrentDigest, &k.CurrentSize, &k.CreatedAt, &k.UpdatedAt, &k.MachineCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

// PublishVersion stores a new version and makes it current. If the digest equals
// the current version's digest, nothing is written and the existing version is returned.
func (s *Store) PublishVersion(ctx context.Context, name, description, digest string, size int64, archive []byte, note, by string) (*SkillVersion, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	ts := now()
	_, err = tx.ExecContext(ctx, `INSERT INTO skills(name,description,created_at,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET description=CASE WHEN excluded.description<>'' THEN excluded.description ELSE skills.description END, updated_at=excluded.updated_at`,
		name, description, ts, ts)
	if err != nil {
		return nil, false, err
	}
	var curID sql.NullInt64
	var curDigest sql.NullString
	var curVer sql.NullInt64
	_ = tx.QueryRowContext(ctx, `SELECT v.id, v.digest, v.version FROM skills s JOIN skill_versions v ON v.id=s.current_version_id WHERE s.name=?`, name).Scan(&curID, &curDigest, &curVer)
	if curDigest.Valid && curDigest.String == digest {
		v := &SkillVersion{ID: curID.Int64, SkillName: name, Version: int(curVer.Int64), Digest: digest, Size: size}
		return v, false, tx.Commit()
	}
	var maxVer int
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM skill_versions WHERE skill_name=?`, name).Scan(&maxVer)
	v := &SkillVersion{SkillName: name, Version: maxVer + 1, Digest: digest, Size: size, Note: note, CreatedBy: by, CreatedAt: ts}
	res, err := tx.ExecContext(ctx, `INSERT INTO skill_versions(skill_name,version,digest,size,archive,note,created_by,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		name, v.Version, digest, size, archive, note, by, ts)
	if err != nil {
		return nil, false, err
	}
	v.ID, _ = res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `UPDATE skills SET current_version_id=?, updated_at=? WHERE name=?`, v.ID, ts, name); err != nil {
		return nil, false, err
	}
	return v, true, tx.Commit()
}

func (s *Store) ListVersions(ctx context.Context, name string) ([]*SkillVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,skill_name,version,digest,size,note,created_by,created_at FROM skill_versions WHERE skill_name=? ORDER BY version DESC`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SkillVersion
	for rows.Next() {
		v := &SkillVersion{}
		if err := rows.Scan(&v.ID, &v.SkillName, &v.Version, &v.Digest, &v.Size, &v.Note, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetVersion(ctx context.Context, id int64) (*SkillVersion, []byte, error) {
	v := &SkillVersion{}
	var archive []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,skill_name,version,digest,size,note,created_by,created_at,archive FROM skill_versions WHERE id=?`, id).
		Scan(&v.ID, &v.SkillName, &v.Version, &v.Digest, &v.Size, &v.Note, &v.CreatedBy, &v.CreatedAt, &archive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	return v, archive, err
}

func (s *Store) SetCurrentVersion(ctx context.Context, name string, versionID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE skills SET current_version_id=?, updated_at=? WHERE name=? AND EXISTS(SELECT 1 FROM skill_versions WHERE id=? AND skill_name=?)`,
		versionID, now(), name, versionID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateSkillDescription(ctx context.Context, name, desc string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE skills SET description=?, updated_at=? WHERE name=?`, desc, now(), name)
	return err
}

func (s *Store) DeleteSkill(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM skills WHERE name=?`, name)
	return err
}

// ---- assignments ----

func (s *Store) Assign(ctx context.Context, machineID, skill string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO assignments(machine_id,skill_name,created_at) VALUES(?,?,?)`, machineID, skill, now())
	return err
}

func (s *Store) Unassign(ctx context.Context, machineID, skill string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM assignments WHERE machine_id=? AND skill_name=?`, machineID, skill)
	return err
}

// AssignedSkills returns the current version of every skill assigned to a machine.
func (s *Store) AssignedSkills(ctx context.Context, machineID string) ([]*SkillVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT v.id,v.skill_name,v.version,v.digest,v.size,v.note,v.created_by,v.created_at
		FROM assignments a JOIN skills s ON s.name=a.skill_name JOIN skill_versions v ON v.id=s.current_version_id
		WHERE a.machine_id=? ORDER BY v.skill_name`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SkillVersion
	for rows.Next() {
		v := &SkillVersion{}
		if err := rows.Scan(&v.ID, &v.SkillName, &v.Version, &v.Digest, &v.Size, &v.Note, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SkillMachines lists machine IDs a skill is assigned to.
func (s *Store) SkillMachines(ctx context.Context, skill string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT machine_id FROM assignments WHERE skill_name=?`, skill)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AllAssignments returns machine_id -> []skill_name.
func (s *Store) AllAssignments(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT machine_id, skill_name FROM assignments ORDER BY skill_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var m, k string
		if err := rows.Scan(&m, &k); err != nil {
			return nil, err
		}
		out[m] = append(out[m], k)
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
	res, err := s.db.ExecContext(ctx, `INSERT INTO jobs(machine_id,type,payload,created_at) VALUES(?,?,?,?)`, machineID, typ, string(b), now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Job{ID: id, MachineID: machineID, Type: typ, Payload: b, Status: "queued"}, nil
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

func scanJobs(rows *sql.Rows) ([]*Job, error) {
	var out []*Job
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO sync_logs(machine_id,at,summary) VALUES(?,?,?)`, machineID, now(), string(summary))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sync_logs WHERE machine_id=? AND id NOT IN (SELECT id FROM sync_logs WHERE machine_id=? ORDER BY id DESC LIMIT 200)`, machineID, machineID)
	return err
}

func (s *Store) ListSyncLogs(ctx context.Context, machineID string, limit int) ([]*SyncLog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,machine_id,at,summary FROM sync_logs WHERE machine_id=? ORDER BY id DESC LIMIT ?`, machineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SyncLog
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
