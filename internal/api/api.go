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
	"github.com/Ken-Chy129/agentdeck/internal/secret"
	"github.com/Ken-Chy129/agentdeck/internal/store"
)

type Server struct {
	st         *store.Store
	box        *secret.Box
	adminToken string
	npmLatest  *npmCache
}

func New(st *store.Store, box *secret.Box, adminToken string) *Server {
	return &Server{st: st, box: box, adminToken: adminToken, npmLatest: newNpmCache()}
}

const maxUpload = 50 << 20

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func (s *Server) Register(mux *http.ServeMux) {
	// agent
	mux.HandleFunc("POST /api/agent/enroll", s.enroll)
	mux.HandleFunc("POST /api/agent/sync", s.withMachine(s.sync))
	mux.HandleFunc("POST /api/agent/report", s.withMachine(s.report))
	mux.HandleFunc("GET /api/agent/versions/{id}/archive", s.withMachine(s.serveArchive))
	mux.HandleFunc("POST /api/agent/skills/{name}", s.withMachine(s.agentPublishSkill))

	// admin: machines
	mux.HandleFunc("GET /api/admin/me", s.withAdmin(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) }))
	mux.HandleFunc("GET /api/admin/overview", s.withAdmin(s.overview))
	mux.HandleFunc("POST /api/admin/enroll-tokens", s.withAdmin(s.createEnrollToken))
	mux.HandleFunc("GET /api/admin/machines/{id}", s.withAdmin(s.getMachine))
	mux.HandleFunc("PATCH /api/admin/machines/{id}", s.withAdmin(s.patchMachine))
	mux.HandleFunc("DELETE /api/admin/machines/{id}", s.withAdmin(s.deleteMachine))
	mux.HandleFunc("GET /api/admin/machines/{id}/logs", s.withAdmin(s.machineLogs))
	mux.HandleFunc("GET /api/admin/machines/{id}/jobs", s.withAdmin(s.machineJobs))
	mux.HandleFunc("POST /api/admin/machines/{id}/jobs", s.withAdmin(s.createJob))
	mux.HandleFunc("POST /api/admin/machines/{id}/import-env", s.withAdmin(s.requestImport))
	mux.HandleFunc("DELETE /api/admin/jobs/{id}", s.withAdmin(s.cancelJob))
	mux.HandleFunc("GET /api/admin/configs", s.withAdmin(s.allConfigs))
	mux.HandleFunc("GET /api/admin/npm-latest", s.withAdmin(s.npmLatestHandler))
	mux.HandleFunc("GET /api/admin/audit", s.withAdmin(s.audit))

	// admin: resources (generic)
	mux.HandleFunc("GET /api/admin/resources", s.withAdmin(s.listResources))
	mux.HandleFunc("GET /api/admin/resources/{id}", s.withAdmin(s.getResource))
	mux.HandleFunc("PATCH /api/admin/resources/{id}", s.withAdmin(s.patchResource))
	mux.HandleFunc("DELETE /api/admin/resources/{id}", s.withAdmin(s.deleteResource))
	mux.HandleFunc("POST /api/admin/resources/{id}/rollback", s.withAdmin(s.rollback))
	mux.HandleFunc("GET /api/admin/resources/{id}/reveal", s.withAdmin(s.reveal))
	mux.HandleFunc("GET /api/admin/versions/{id}/files", s.withAdmin(s.versionFiles))
	mux.HandleFunc("GET /api/admin/versions/{id}/archive", s.withAdmin(s.serveArchive))
	mux.HandleFunc("PUT /api/admin/assignments", s.withAdmin(s.setAssignment))

	// admin: kind-specific publish
	mux.HandleFunc("POST /api/admin/skills/{name}", s.withAdmin(s.adminPublishSkill))      // tar.gz body
	mux.HandleFunc("PUT /api/admin/skills/{name}/files", s.withAdmin(s.publishSkillFiles)) // json files
	mux.HandleFunc("PUT /api/admin/env/{name}", s.withAdmin(s.publishEnv))
	mux.HandleFunc("PUT /api/admin/config/{name}", s.withAdmin(s.publishConfig))
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

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func metaOf(res *store.Resource) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(res.Meta, &m)
	return m
}

func metaStr(res *store.Resource, k string) string {
	v, _ := metaOf(res)[k].(string)
	return v
}

func metaBool(res *store.Resource, k string) bool {
	v, _ := metaOf(res)[k].(bool)
	return v
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
	s.st.Audit(r.Context(), "machine:"+m.Name, "enroll", m.ID, "")
	writeJSON(w, 200, protocol.EnrollResponse{MachineID: m.ID, MachineToken: tok, Name: m.Name})
}

// pendingImports: machine_id -> env names the console asked to import (in-memory; a
// sync cycle later than 15 min after the click is fine to lose).
var pendingImports = map[string]map[string]bool{}

func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	m := machineFrom(r)
	var req protocol.SyncRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	if req.Inventory != nil {
		inv, _ := json.Marshal(req.Inventory)
		local, _ := json.Marshal(req.LocalSkills)
		if err := s.st.SaveInventory(ctx, m.ID, inv, local); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.Snapshot != nil {
		snap, _ := json.Marshal(req.Snapshot)
		_ = s.st.SaveSnapshot(ctx, m.ID, snap)
	}
	for _, a := range req.Applied {
		_ = s.st.UpsertMachineResource(ctx, store.MachineResource{MachineID: m.ID, ResourceID: a.ID, Digest: a.Digest, Status: "reported"})
	}

	desired, err := s.st.DesiredFor(ctx, m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	resp := protocol.SyncResponse{MachineID: m.ID, MachineName: m.Name, Resources: []protocol.DesiredResource{}, Jobs: []protocol.Job{}}
	for _, d := range desired {
		dr := protocol.DesiredResource{ID: d.Resource.ID, Kind: d.Resource.Kind, Name: d.Resource.Name, VersionID: d.Version.ID, Version: d.Version.Version, Digest: d.Version.Digest, Size: d.Version.Size}
		switch d.Resource.Kind {
		case "env":
			val := string(d.Content)
			if d.Override != nil {
				val = *d.Override
			}
			plain, err := s.box.Open(val)
			if err != nil {
				log.Printf("env %s: cannot open sealed value: %v", d.Resource.Name, err)
				continue
			}
			dr.Value = plain
			dr.Secret = metaBool(d.Resource, "secret")
			dr.Digest = store.Digest([]byte(plain))
		case "config":
			dr.Content = string(d.Content)
			dr.Tool = metaStr(d.Resource, "tool")
			dr.Path = metaStr(d.Resource, "path")
			dr.Format = metaStr(d.Resource, "format")
			if d.Override != nil {
				dr.Override = *d.Override
				dr.Digest = store.Digest([]byte(dr.Content + "\x00" + dr.Override))
			}
		}
		resp.Resources = append(resp.Resources, dr)
	}
	jobs, err := s.st.QueuedJobs(ctx, m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, j := range jobs {
		resp.Jobs = append(resp.Jobs, protocol.Job{ID: j.ID, Type: j.Type, Payload: j.Payload})
	}
	if p := pendingImports[m.ID]; len(p) > 0 {
		for n := range p {
			resp.ImportEnv = append(resp.ImportEnv, n)
		}
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
	ctx := r.Context()
	for _, j := range rep.Jobs {
		st := "failed"
		if j.Status == "done" {
			st = "done"
		}
		_ = s.st.FinishJob(ctx, m.ID, j.ID, st, j.Output)
	}
	for _, rr := range rep.Resources {
		if rr.Action == "removed" {
			continue
		}
		_ = s.st.UpsertMachineResource(ctx, store.MachineResource{MachineID: m.ID, ResourceID: rr.ID, Digest: rr.Digest, Status: rr.Action, Detail: firstNonEmpty(rr.Error, rr.Detail)})
	}
	// imported env values: seal and store as new env resources (or new versions), assigned to this machine
	for name, val := range rep.Imported {
		if !envNameRe.MatchString(name) || val == "" {
			continue
		}
		sealed := s.box.Seal(val)
		res, _, created, err := s.st.Publish(ctx, "env", name, map[string]any{"secret": looksSecret(name, val)}, store.Digest([]byte(val)), []byte(sealed), "imported from "+m.Name, "machine:"+m.Name)
		if err != nil {
			log.Printf("import env %s: %v", name, err)
			continue
		}
		_ = s.st.Assign(ctx, m.ID, res.ID)
		delete(pendingImports[m.ID], name)
		s.st.Audit(ctx, "machine:"+m.Name, "env.import", name, fmt.Sprintf("created=%v", created))
	}
	rep.Imported = nil // never persist real values in logs
	b, _ := json.Marshal(rep)
	if err := s.st.AddSyncLog(ctx, m.ID, b); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var secretNameRe = regexp.MustCompile(`(?i)(key|token|secret|password|passwd|credential)`)

func looksSecret(name, val string) bool {
	return secretNameRe.MatchString(name) || strings.HasPrefix(val, "sk-") || strings.HasPrefix(val, "gh") && len(val) > 30
}

func (s *Server) serveArchive(w http.ResponseWriter, r *http.Request) {
	v, content, err := s.st.GetVersion(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, 404, "version not found")
		return
	}
	res, err := s.st.GetResource(r.Context(), v.ResourceID)
	if err != nil || res.Kind != "skill" {
		writeErr(w, 404, "not a skill version")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-Digest", v.Digest)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-v%d.tar.gz"`, res.Name, v.Version))
	w.Write(content)
}

// ---- publish: skill ----

func (s *Server) publishSkillArchive(w http.ResponseWriter, r *http.Request, by string) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) || strings.Contains(name, "/") {
		writeErr(w, 400, "invalid skill name")
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
	s.storeSkill(w, r, name, set, r.URL.Query().Get("note"), r.URL.Query().Get("description"), by)
}

func (s *Server) storeSkill(w http.ResponseWriter, r *http.Request, name string, set *bundle.Set, note, desc, by string) {
	if err := set.ValidateSkill(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	canon, err := set.Pack()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if desc == "" {
		desc = set.Description()
	}
	res, v, created, err := s.st.Publish(r.Context(), "skill", name, map[string]any{"description": desc}, set.Digest(), canon, note, by)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ids := r.URL.Query().Get("assign"); ids != "" {
		for _, id := range strings.Split(ids, ",") {
			if id == "self" {
				if m := machineFrom(r); m != nil {
					id = m.ID
				}
			}
			_ = s.st.Assign(r.Context(), id, res.ID)
		}
	}
	if created {
		s.st.Audit(r.Context(), by, "skill.publish", name, fmt.Sprintf("v%d", v.Version))
	}
	writeJSON(w, 200, map[string]any{"resource": res, "version": v, "created": created})
}

func (s *Server) agentPublishSkill(w http.ResponseWriter, r *http.Request) {
	s.publishSkillArchive(w, r, "machine:"+machineFrom(r).Name)
}

func (s *Server) adminPublishSkill(w http.ResponseWriter, r *http.Request) {
	s.publishSkillArchive(w, r, "admin")
}

func (s *Server) publishSkillFiles(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) || strings.Contains(name, "/") {
		writeErr(w, 400, "invalid skill name")
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
	s.storeSkill(w, r, name, set, req.Note, req.Description, "admin")
}

// ---- publish: env ----

func (s *Server) publishEnv(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !envNameRe.MatchString(name) {
		writeErr(w, 400, "invalid variable name")
		return
	}
	var req struct {
		Value       *string `json:"value"`
		Secret      *bool   `json:"secret"`
		Description string  `json:"description"`
		Note        string  `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	meta := map[string]any{}
	if req.Secret != nil {
		meta["secret"] = *req.Secret
	}
	if req.Description != "" {
		meta["description"] = req.Description
	}
	var content []byte
	digest := ""
	if req.Value != nil {
		if req.Secret == nil {
			meta["secret"] = looksSecret(name, *req.Value)
		}
		digest = store.Digest([]byte(*req.Value))
		content = []byte(s.box.Seal(*req.Value))
	}
	res, v, created, err := s.st.Publish(r.Context(), "env", name, meta, digest, content, req.Note, "admin")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if created {
		s.st.Audit(r.Context(), "admin", "env.set", name, fmt.Sprintf("v%d", v.Version))
	}
	writeJSON(w, 200, map[string]any{"resource": res, "version": v, "created": created})
}

// ---- publish: config ----

var allowedFormats = map[string]bool{"json": true, "toml": true, "yaml": true}

func (s *Server) publishConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, 400, "invalid config name")
		return
	}
	var req struct {
		Content     *string `json:"content"`
		Tool        string  `json:"tool"`
		Path        string  `json:"path"`
		Format      string  `json:"format"`
		Description string  `json:"description"`
		Note        string  `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	meta := map[string]any{}
	if req.Tool != "" {
		meta["tool"] = req.Tool
	}
	if req.Path != "" {
		if !strings.HasPrefix(req.Path, "~/") {
			writeErr(w, 400, "path must start with ~/")
			return
		}
		meta["path"] = req.Path
	}
	if req.Format != "" {
		if !allowedFormats[req.Format] {
			writeErr(w, 400, "format must be json|toml|yaml")
			return
		}
		meta["format"] = req.Format
	}
	if req.Description != "" {
		meta["description"] = req.Description
	}
	var content []byte
	digest := ""
	if req.Content != nil {
		content = []byte(*req.Content)
		digest = store.Digest(content)
	}
	res, v, created, err := s.st.Publish(r.Context(), "config", name, meta, digest, content, req.Note, "admin")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if metaStr(res, "path") == "" || metaStr(res, "format") == "" {
		writeErr(w, 400, "config needs path and format")
		return
	}
	if created {
		s.st.Audit(r.Context(), "admin", "config.publish", name, fmt.Sprintf("v%d", v.Version))
	}
	writeJSON(w, 200, map[string]any{"resource": res, "version": v, "created": created})
}

// ---- admin: machines ----

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
	ctx := r.Context()
	machines, err := s.st.ListMachines(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, m := range machines {
		m.Snapshot = nil
	}
	resources, err := s.st.ListResources(ctx, "")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	assigns, _ := s.st.AllAssignments(ctx)
	for _, a := range assigns {
		a.Override = "" // never leak sealed/override values in bulk
	}
	applied, _ := s.st.AllMachineResources(ctx)
	writeJSON(w, 200, map[string]any{"machines": machines, "resources": resources, "assignments": assigns, "applied": applied, "now": time.Now().UTC().Format(time.RFC3339)})
}

func (s *Server) getMachine(w http.ResponseWriter, r *http.Request) {
	m, err := s.st.MachineByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "machine not found")
		return
	}
	writeJSON(w, 200, m)
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
	s.st.Audit(r.Context(), "admin", "machine.delete", r.PathValue("id"), "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) machineLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.st.ListSyncLogs(r.Context(), r.PathValue("id"), 50)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, logs)
}

func (s *Server) machineJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.st.ListJobs(r.Context(), r.PathValue("id"), 50)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
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
	s.st.Audit(r.Context(), "admin", "job.create", r.PathValue("id"), req.Type+" "+string(req.Payload))
	writeJSON(w, 200, j)
}

func (s *Server) requestImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"names"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := r.PathValue("id")
	if pendingImports[id] == nil {
		pendingImports[id] = map[string]bool{}
	}
	for _, n := range req.Names {
		if envNameRe.MatchString(n) {
			pendingImports[id][n] = true
		}
	}
	s.st.Audit(r.Context(), "admin", "env.import.request", id, strings.Join(req.Names, ","))
	writeJSON(w, 200, map[string]any{"pending": len(pendingImports[id])})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if err := s.st.CancelJob(r.Context(), pathID(r)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

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

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	a, err := s.st.ListAudit(r.Context(), 200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, a)
}

// ---- admin: resources ----

func (s *Server) listResources(w http.ResponseWriter, r *http.Request) {
	rs, err := s.st.ListResources(r.Context(), r.URL.Query().Get("kind"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rs)
}

func (s *Server) getResource(w http.ResponseWriter, r *http.Request) {
	res, err := s.st.GetResource(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, 404, "resource not found")
		return
	}
	vs, _ := s.st.ListVersions(r.Context(), res.ID)
	assigns, _ := s.st.AllAssignments(r.Context())
	mine := []*store.Assignment{}
	for _, a := range assigns {
		if a.ResourceID == res.ID {
			if res.Kind == "env" {
				a.Override = "" // sealed; use /reveal
			}
			mine = append(mine, a)
		}
	}
	out := map[string]any{"resource": res, "versions": vs, "assignments": mine}
	if res.Kind == "config" {
		if _, content, err := s.st.CurrentContent(r.Context(), res.ID); err == nil {
			out["content"] = string(content)
		}
	}
	applied, _ := s.st.AllMachineResources(r.Context())
	ap := []*store.MachineResource{}
	for _, a := range applied {
		if a.ResourceID == res.ID {
			ap = append(ap, a)
		}
	}
	out["applied"] = ap
	writeJSON(w, 200, out)
}

// reveal decrypts an env value (or a machine override) — audited.
func (s *Server) reveal(w http.ResponseWriter, r *http.Request) {
	res, err := s.st.GetResource(r.Context(), pathID(r))
	if err != nil || res.Kind != "env" {
		writeErr(w, 404, "env not found")
		return
	}
	var sealed string
	target := res.Name
	if mid := r.URL.Query().Get("machine"); mid != "" {
		assigns, _ := s.st.AllAssignments(r.Context())
		for _, a := range assigns {
			if a.ResourceID == res.ID && a.MachineID == mid && a.HasOverr {
				sealed = a.Override
				target += "@" + mid
			}
		}
		if sealed == "" {
			writeErr(w, 404, "no override for that machine")
			return
		}
	} else {
		_, content, err := s.st.CurrentContent(r.Context(), res.ID)
		if err != nil {
			writeErr(w, 404, "no value")
			return
		}
		sealed = string(content)
	}
	plain, err := s.box.Open(sealed)
	if err != nil {
		writeErr(w, 500, "cannot decrypt: "+err.Error())
		return
	}
	s.st.Audit(r.Context(), "admin", "env.reveal", target, r.RemoteAddr)
	writeJSON(w, 200, map[string]string{"value": plain})
}

func (s *Server) patchResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string         `json:"name"`
		Meta map[string]any `json:"meta"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := pathID(r)
	if req.Name != "" {
		if err := s.st.RenameResource(r.Context(), id, req.Name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if len(req.Meta) > 0 {
		if err := s.st.UpdateMeta(r.Context(), id, req.Meta); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteResource(w http.ResponseWriter, r *http.Request) {
	res, err := s.st.GetResource(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	if err := s.st.DeleteResource(r.Context(), res.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.st.Audit(r.Context(), "admin", res.Kind+".delete", res.Name, "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VersionID int64 `json:"version_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.st.SetCurrentVersion(r.Context(), pathID(r), req.VersionID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "version not found")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) versionFiles(w http.ResponseWriter, r *http.Request) {
	v, content, err := s.st.GetVersion(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, 404, "version not found")
		return
	}
	res, _ := s.st.GetResource(r.Context(), v.ResourceID)
	if res == nil || res.Kind != "skill" {
		writeErr(w, 400, "not a skill version")
		return
	}
	set, err := bundle.Unpack(bytes.NewReader(content))
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
		if bytes.IndexByte(f.Content, 0) < 0 && len(f.Content) <= 512*1024 {
			fo.Text = string(f.Content)
		} else {
			fo.Binary = true
		}
		out = append(out, fo)
	}
	writeJSON(w, 200, map[string]any{"version": v, "files": out})
}

// setAssignment: {machine_id, resource_id, assigned, override?}
// override: for env the plaintext machine-specific value (sealed here); for config the override text.
// Pass "override": null to clear.
func (s *Server) setAssignment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MachineID  string  `json:"machine_id"`
		ResourceID int64   `json:"resource_id"`
		Assigned   bool    `json:"assigned"`
		Override   *string `json:"override"`
		SetOverr   bool    `json:"set_override"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	res, err := s.st.GetResource(r.Context(), req.ResourceID)
	if err != nil {
		writeErr(w, 404, "resource not found")
		return
	}
	if !req.Assigned {
		if err := s.st.Unassign(r.Context(), req.MachineID, req.ResourceID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		s.st.Audit(r.Context(), "admin", "unassign", res.Kind+"/"+res.Name, req.MachineID)
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if req.SetOverr {
		var ov *string
		if req.Override != nil && *req.Override != "" {
			v := *req.Override
			if res.Kind == "env" {
				v = s.box.Seal(v)
			}
			ov = &v
		}
		if err := s.st.SetOverride(r.Context(), req.MachineID, req.ResourceID, ov); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		s.st.Audit(r.Context(), "admin", "override", res.Kind+"/"+res.Name, req.MachineID)
	} else if err := s.st.Assign(r.Context(), req.MachineID, req.ResourceID); err != nil {
		writeErr(w, 500, err.Error())
		return
	} else {
		s.st.Audit(r.Context(), "admin", "assign", res.Kind+"/"+res.Name, req.MachineID)
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
