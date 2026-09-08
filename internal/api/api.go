// Package api exposes the HTTP surface: /api/admin/* for the web console
// (admin token) and /api/agent/* for machines (per-machine token).
package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Ken-Chy129/agentdeck/internal/bundle"
	"github.com/Ken-Chy129/agentdeck/internal/protocol"
	"github.com/Ken-Chy129/agentdeck/internal/store"
)

type Server struct {
	st         *store.Store
	adminToken string
	npmLatest  *npmCache
}

func New(st *store.Store, adminToken string) *Server {
	return &Server{st: st, adminToken: adminToken, npmLatest: newNpmCache()}
}

const maxUpload = 50 << 20

var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func (s *Server) Register(mux *http.ServeMux) {
	// agent
	mux.HandleFunc("POST /api/agent/enroll", s.enroll)
	mux.HandleFunc("POST /api/agent/sync", s.withMachine(s.sync))
	mux.HandleFunc("POST /api/agent/report", s.withMachine(s.report))
	mux.HandleFunc("GET /api/agent/skills/{name}/versions/{id}/archive", s.withMachine(s.agentArchive))
	mux.HandleFunc("POST /api/agent/skills/{name}", s.withMachine(s.agentPublish))
	mux.HandleFunc("GET /api/agent/skills", s.withMachine(s.agentListSkills))

	// admin
	mux.HandleFunc("GET /api/admin/me", s.withAdmin(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) }))
	mux.HandleFunc("GET /api/admin/overview", s.withAdmin(s.overview))
	mux.HandleFunc("POST /api/admin/enroll-tokens", s.withAdmin(s.createEnrollToken))
	mux.HandleFunc("GET /api/admin/machines", s.withAdmin(s.listMachines))
	mux.HandleFunc("GET /api/admin/machines/{id}", s.withAdmin(s.getMachine))
	mux.HandleFunc("PATCH /api/admin/machines/{id}", s.withAdmin(s.patchMachine))
	mux.HandleFunc("DELETE /api/admin/machines/{id}", s.withAdmin(s.deleteMachine))
	mux.HandleFunc("GET /api/admin/machines/{id}/logs", s.withAdmin(s.machineLogs))
	mux.HandleFunc("GET /api/admin/configs", s.withAdmin(s.allConfigs))
	mux.HandleFunc("GET /api/admin/machines/{id}/jobs", s.withAdmin(s.machineJobs))
	mux.HandleFunc("POST /api/admin/machines/{id}/jobs", s.withAdmin(s.createJob))
	mux.HandleFunc("DELETE /api/admin/jobs/{id}", s.withAdmin(s.cancelJob))
	mux.HandleFunc("GET /api/admin/skills", s.withAdmin(s.listSkills))
	mux.HandleFunc("POST /api/admin/skills/{name}", s.withAdmin(s.adminPublish))
	mux.HandleFunc("PUT /api/admin/skills/{name}/files", s.withAdmin(s.publishFiles))
	mux.HandleFunc("GET /api/admin/skills/{name}", s.withAdmin(s.getSkill))
	mux.HandleFunc("PATCH /api/admin/skills/{name}", s.withAdmin(s.patchSkill))
	mux.HandleFunc("DELETE /api/admin/skills/{name}", s.withAdmin(s.deleteSkill))
	mux.HandleFunc("GET /api/admin/skills/{name}/versions/{id}/files", s.withAdmin(s.versionFiles))
	mux.HandleFunc("GET /api/admin/skills/{name}/versions/{id}/archive", s.withAdmin(s.adminArchive))
	mux.HandleFunc("POST /api/admin/skills/{name}/rollback", s.withAdmin(s.rollback))
	mux.HandleFunc("PUT /api/admin/assignments", s.withAdmin(s.setAssignment))
	mux.HandleFunc("GET /api/admin/npm-latest", s.withAdmin(s.npmLatestHandler))
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (s *Server) withAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.adminToken)) != 1 {
			writeErr(w, 401, "unauthorized")
			return
		}
		h(w, r)
	}
}

type ctxKey int

const machineKey ctxKey = 1

func machineFrom(r *http.Request) *store.Machine {
	m, _ := r.Context().Value(machineKey).(*store.Machine)
	return m
}

func (s *Server) withMachine(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			writeErr(w, 401, "missing machine token")
			return
		}
		m, err := s.st.MachineByToken(r.Context(), tok)
		if err != nil {
			writeErr(w, 401, "invalid machine token")
			return
		}
		_ = s.st.TouchMachine(r.Context(), m.ID)
		h(w, r.WithContext(context.WithValue(r.Context(), machineKey, m)))
	}
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(v)
}

// ---- agent ----

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var req protocol.EnrollRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, 400, "name required")
		return
	}
	m, tok, err := s.st.Enroll(r.Context(), req.EnrollToken, req.Name, req.OS, req.Arch, req.Hostname)
	if err != nil {
		writeErr(w, 403, err.Error())
		return
	}
	log.Printf("enrolled machine %s (%s)", m.Name, m.ID)
	writeJSON(w, 200, protocol.EnrollResponse{MachineID: m.ID, MachineToken: tok, Name: m.Name})
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	m := machineFrom(r)
	var req protocol.SyncRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Inventory != nil {
		inv, _ := json.Marshal(req.Inventory)
		local, _ := json.Marshal(req.LocalSkills)
		if err := s.st.SaveInventory(r.Context(), m.ID, inv, local); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.Snapshot != nil {
		snap, _ := json.Marshal(req.Snapshot)
		if err := s.st.SaveSnapshot(r.Context(), m.ID, snap); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	assigned, err := s.st.AssignedSkills(r.Context(), m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	resp := protocol.SyncResponse{MachineID: m.ID, MachineName: m.Name, Skills: []protocol.DesiredSkill{}, Jobs: []protocol.Job{}}
	for _, v := range assigned {
		resp.Skills = append(resp.Skills, protocol.DesiredSkill{Name: v.SkillName, VersionID: v.ID, Version: v.Version, Digest: v.Digest, Size: v.Size})
	}
	jobs, err := s.st.QueuedJobs(r.Context(), m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, j := range jobs {
		resp.Jobs = append(resp.Jobs, protocol.Job{ID: j.ID, Type: j.Type, Payload: j.Payload})
	}
	writeJSON(w, 200, resp)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	m := machineFrom(r)
	var rep protocol.SyncReport
	if err := decode(r, &rep); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	for _, j := range rep.Jobs {
		st := "failed"
		if j.Status == "done" {
			st = "done"
		}
		_ = s.st.FinishJob(r.Context(), m.ID, j.ID, st, j.Output)
	}
	b, _ := json.Marshal(rep)
	if err := s.st.AddSyncLog(r.Context(), m.ID, b); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) serveArchive(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	v, archive, err := s.st.GetVersion(r.Context(), id)
	if err != nil || v.SkillName != r.PathValue("name") {
		writeErr(w, 404, "version not found")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-Skill-Digest", v.Digest)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-v%d.tar.gz"`, v.SkillName, v.Version))
	w.Write(archive)
}

func (s *Server) agentArchive(w http.ResponseWriter, r *http.Request) { s.serveArchive(w, r) }
func (s *Server) adminArchive(w http.ResponseWriter, r *http.Request) { s.serveArchive(w, r) }

func (s *Server) agentListSkills(w http.ResponseWriter, r *http.Request) { s.listSkills(w, r) }

// publish accepts a tar.gz body and stores it as a new version if changed.
func (s *Server) publish(w http.ResponseWriter, r *http.Request, by string) {
	name := r.PathValue("name")
	if !skillNameRe.MatchString(name) {
		writeErr(w, 400, "invalid skill name (lowercase, digits, . _ -)")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxUpload+1))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(body) > maxUpload {
		writeErr(w, 413, "archive too large")
		return
	}
	set, err := bundle.Unpack(bytes.NewReader(body))
	if err != nil {
		writeErr(w, 400, "bad archive: "+err.Error())
		return
	}
	if err := set.ValidateSkill(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// Re-pack so stored bytes are canonical regardless of who produced the upload.
	canon, err := set.Pack()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	note := r.URL.Query().Get("note")
	desc := r.URL.Query().Get("description")
	if desc == "" {
		desc = set.Description()
	}
	v, created, err := s.st.PublishVersion(r.Context(), name, desc, set.Digest(), set.Size(), canon, note, by)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Optional: assign to machines right away.
	if ids := r.URL.Query().Get("assign"); ids != "" {
		for _, id := range strings.Split(ids, ",") {
			if id == "self" {
				if m := machineFrom(r); m != nil {
					id = m.ID
				}
			}
			_ = s.st.Assign(r.Context(), id, name)
		}
	}
	writeJSON(w, 200, map[string]any{"version": v, "created": created})
}

func (s *Server) agentPublish(w http.ResponseWriter, r *http.Request) {
	s.publish(w, r, "machine:"+machineFrom(r).Name)
}

func (s *Server) adminPublish(w http.ResponseWriter, r *http.Request) {
	s.publish(w, r, "admin")
}

// ---- admin ----

func (s *Server) createEnrollToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note string `json:"note"`
	}
	_ = decode(r, &req)
	tok, err := s.st.CreateEnrollToken(r.Context(), req.Note)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"enroll_token": tok})
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	machines, err := s.st.ListMachines(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	skills, err := s.st.ListSkills(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	assign, _ := s.st.AllAssignments(r.Context())
	if machines == nil {
		machines = []*store.Machine{}
	}
	for _, m := range machines {
		m.Snapshot = nil
	}
	if skills == nil {
		skills = []*store.Skill{}
	}
	writeJSON(w, 200, map[string]any{"machines": machines, "skills": skills, "assignments": assign, "now": time.Now().UTC().Format(time.RFC3339)})
}

func (s *Server) listMachines(w http.ResponseWriter, r *http.Request) {
	ms, err := s.st.ListMachines(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ms == nil {
		ms = []*store.Machine{}
	}
	writeJSON(w, 200, ms)
}

func (s *Server) getMachine(w http.ResponseWriter, r *http.Request) {
	m, err := s.st.MachineByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "machine not found")
		return
	}
	assigned, _ := s.st.AssignedSkills(r.Context(), m.ID)
	if assigned == nil {
		assigned = []*store.SkillVersion{}
	}
	writeJSON(w, 200, map[string]any{"machine": m, "assigned": assigned})
}

func (s *Server) patchMachine(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, 400, "name required")
		return
	}
	if err := s.st.RenameMachine(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Name)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteMachine(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteMachine(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) machineLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.st.ListSyncLogs(r.Context(), r.PathValue("id"), 50)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if logs == nil {
		logs = []*store.SyncLog{}
	}
	writeJSON(w, 200, logs)
}

func (s *Server) machineJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.st.ListJobs(r.Context(), r.PathValue("id"), 50)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if jobs == nil {
		jobs = []*store.Job{}
	}
	writeJSON(w, 200, jobs)
}

var allowedJobs = map[string]bool{protocol.JobNpmUpgrade: true, protocol.JobBrewUpgrade: true, protocol.JobEcho: true}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !allowedJobs[req.Type] {
		writeErr(w, 400, "unsupported job type")
		return
	}
	if _, err := s.st.MachineByID(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, 404, "machine not found")
		return
	}
	j, err := s.st.CreateJob(r.Context(), r.PathValue("id"), req.Type, req.Payload)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, j)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.st.CancelJob(r.Context(), id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	ks, err := s.st.ListSkills(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ks == nil {
		ks = []*store.Skill{}
	}
	writeJSON(w, 200, ks)
}

func (s *Server) getSkill(w http.ResponseWriter, r *http.Request) {
	k, err := s.st.GetSkill(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, "skill not found")
		return
	}
	vs, _ := s.st.ListVersions(r.Context(), k.Name)
	if vs == nil {
		vs = []*store.SkillVersion{}
	}
	ms, _ := s.st.SkillMachines(r.Context(), k.Name)
	if ms == nil {
		ms = []string{}
	}
	writeJSON(w, 200, map[string]any{"skill": k, "versions": vs, "machines": ms})
}

func (s *Server) patchSkill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description *string `json:"description"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Description != nil {
		if err := s.st.UpdateSkillDescription(r.Context(), r.PathValue("name"), *req.Description); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteSkill(r.Context(), r.PathValue("name")); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) versionFiles(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	v, archive, err := s.st.GetVersion(r.Context(), id)
	if err != nil || v.SkillName != r.PathValue("name") {
		writeErr(w, 404, "version not found")
		return
	}
	set, err := bundle.Unpack(bytes.NewReader(archive))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type fileOut struct {
		Path   string `json:"path"`
		Size   int    `json:"size"`
		Mode   uint32 `json:"mode"`
		Text   string `json:"text,omitempty"`
		Binary bool   `json:"binary,omitempty"`
	}
	out := []fileOut{}
	for _, f := range set.Files {
		fo := fileOut{Path: f.Path, Size: len(f.Content), Mode: f.Mode}
		if isText(f.Content) && len(f.Content) <= 512*1024 {
			fo.Text = string(f.Content)
		} else {
			fo.Binary = true
		}
		out = append(out, fo)
	}
	writeJSON(w, 200, map[string]any{"version": v, "files": out})
}

func isText(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	return true
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VersionID int64 `json:"version_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.st.SetCurrentVersion(r.Context(), r.PathValue("name"), req.VersionID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "version not found")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) setAssignment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MachineID string `json:"machine_id"`
		Skill     string `json:"skill"`
		Assigned  bool   `json:"assigned"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var err error
	if req.Assigned {
		err = s.st.Assign(r.Context(), req.MachineID, req.Skill)
	} else {
		err = s.st.Unassign(r.Context(), req.MachineID, req.Skill)
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) npmLatestHandler(w http.ResponseWriter, r *http.Request) {
	pkgs := strings.Split(r.URL.Query().Get("pkgs"), ",")
	out := map[string]string{}
	for _, p := range pkgs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out[p] = s.npmLatest.get(r.Context(), p)
	}
	writeJSON(w, 200, out)
}

// publishFiles lets the web console create/edit a skill without producing a tarball:
// body is {"files":[{"path":"SKILL.md","text":"..."}], "note":"...", "description":"..."}.
func (s *Server) publishFiles(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !skillNameRe.MatchString(name) {
		writeErr(w, 400, "invalid skill name (lowercase, digits, . _ -)")
		return
	}
	var req struct {
		Files []struct {
			Path string `json:"path"`
			Text string `json:"text"`
			Exec bool   `json:"exec"`
		} `json:"files"`
		Note        string `json:"note"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxUpload)).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	set := &bundle.Set{}
	for _, f := range req.Files {
		mode := uint32(0o644)
		if f.Exec {
			mode = 0o755
		}
		set.Files = append(set.Files, bundle.File{Path: f.Path, Mode: mode, Content: []byte(f.Text)})
	}
	set.Normalize()
	if err := set.ValidateSkill(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	canon, err := set.Pack()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	desc := req.Description
	if desc == "" {
		desc = set.Description()
	}
	v, created, err := s.st.PublishVersion(r.Context(), name, desc, set.Digest(), set.Size(), canon, req.Note, "admin")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"version": v, "created": created})
}

// allConfigs returns every machine's redacted snapshot: the "what is configured where" view.
func (s *Server) allConfigs(w http.ResponseWriter, r *http.Request) {
	ms, err := s.st.ListMachines(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type entry struct {
		MachineID   string          `json:"machine_id"`
		MachineName string          `json:"machine_name"`
		SnapshotAt  string          `json:"snapshot_at"`
		Snapshot    json.RawMessage `json:"snapshot"`
	}
	out := []entry{}
	for _, m := range ms {
		out = append(out, entry{MachineID: m.ID, MachineName: m.Name, SnapshotAt: m.SnapshotAt, Snapshot: m.Snapshot})
	}
	writeJSON(w, 200, out)
}
