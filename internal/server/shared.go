package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gantry/internal/engine"
	"gantry/internal/hosts"
	"gantry/internal/labs"
)

// Shared services: databases and brokers that already run on a machine and
// that new projects can use instead of starting their own. The app finds
// them by image, creates a database and a user for each project that asks
// (so projects do not share tables) and remembers who uses what.

// sharedHost is the name containers use to reach a port published on their
// machine. Templates that use shared services map it with
// extra_hosts: ["host.containers.internal:host-gateway"], which works in
// Docker and Podman alike.
const sharedHost = "host.containers.internal"

var sharedPorts = map[string]int{"postgres": 5432, "mysql": 3306, "redis": 6379, "rabbitmq": 5672, "mongo": 27017}

var sharedTitle = map[string]string{"postgres": "PostgreSQL", "mysql": "MySQL / MariaDB", "redis": "Redis", "rabbitmq": "RabbitMQ", "mongo": "MongoDB"}

// sharedKind tells what a container is by its image.
func sharedKind(image string) string {
	im := strings.ToLower(image)
	if i := strings.LastIndex(im, "/"); i >= 0 {
		im = im[i+1:]
	}
	if i := strings.Index(im, "@"); i >= 0 {
		im = im[:i]
	}
	if i := strings.Index(im, ":"); i >= 0 {
		im = im[:i]
	}
	switch {
	case strings.Contains(im, "postgres") || strings.Contains(im, "pgvector") || strings.Contains(im, "postgis") || strings.Contains(im, "timescale"):
		return "postgres"
	case strings.Contains(im, "mariadb") || strings.Contains(im, "mysql"):
		return "mysql"
	case strings.Contains(im, "redis") || strings.Contains(im, "valkey") || strings.Contains(im, "keydb"):
		return "redis"
	case strings.Contains(im, "rabbitmq"):
		return "rabbitmq"
	case strings.Contains(im, "mongo"):
		return "mongo"
	}
	return ""
}

type sharedUse struct {
	User  string `json:"user,omitempty"`
	DB    string `json:"db,omitempty"`
	Since int64  `json:"since"`
}

type sharedSvc struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Kind      string               `json:"kind"`
	Title     string               `json:"title"`
	Image     string               `json:"image"`
	Lab       string               `json:"lab,omitempty"`
	Running   bool                 `json:"running"`
	Port      int                  `json:"port,omitempty"` // published on the machine
	Consumers map[string]sharedUse `json:"consumers"`
}

var sharedMu sync.Mutex

func (s *Server) sharedFile() string { return filepath.Join(s.configDir(), "compartilhados.json") }

// sharedUses: machine -> container name -> project -> use.
func (s *Server) loadShared() map[string]map[string]map[string]sharedUse {
	m := map[string]map[string]map[string]sharedUse{}
	if b, err := os.ReadFile(s.sharedFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (s *Server) recordShared(machine, ctr, project string, u sharedUse) error {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	m := s.loadShared()
	if m[machine] == nil {
		m[machine] = map[string]map[string]sharedUse{}
	}
	if m[machine][ctr] == nil {
		m[machine][ctr] = map[string]sharedUse{}
	}
	u.Since = time.Now().Unix()
	m[machine][ctr][project] = u
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(s.configDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.sharedFile(), b, 0o600)
}

func (s *Server) sharedServices(ctx context.Context, h *hosts.Host) ([]sharedSvc, error) {
	cs, err := h.Engine.Containers(ctx)
	if err != nil {
		return nil, err
	}
	sharedMu.Lock()
	uses := s.loadShared()[h.Machine()]
	sharedMu.Unlock()
	out := []sharedSvc{}
	for _, c := range cs {
		kind := sharedKind(c.Image)
		if kind == "" {
			continue
		}
		sv := sharedSvc{ID: c.ID, Name: c.Name(), Kind: kind, Title: sharedTitle[kind], Image: c.Image,
			Lab: c.Labels[labs.LabelProject], Running: c.State == "running", Consumers: uses[c.Name()]}
		if sv.Consumers == nil {
			sv.Consumers = map[string]sharedUse{}
		}
		for _, p := range c.Ports {
			if p.PrivatePort == sharedPorts[kind] && p.PublicPort > 0 {
				sv.Port = p.PublicPort
				break
			}
		}
		out = append(out, sv)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Server) listShared(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	if h.Engine == nil {
		writeJSON(w, []sharedSvc{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, err := s.sharedServices(ctx, h)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	if k := r.URL.Query().Get("kind"); k != "" {
		keep := out[:0]
		for _, x := range out {
			if x.Kind == k {
				keep = append(keep, x)
			}
		}
		out = keep
	}
	writeJSON(w, out)
}

var identRe = regexp.MustCompile(`[^a-z0-9_]+`)

// sharedIdent turns a project name into a database or user name. The g_
// prefix keeps it away from the service's own accounts (postgres, root,
// admin…) and the hash of the exact name keeps my-app and my_app apart.
func sharedIdent(project string) string {
	s := identRe.ReplaceAllString(strings.ToLower(project), "_")
	s = strings.Trim(s, "_")
	if len(s) > 40 {
		s = s[:40]
	}
	sum := sha256.Sum256([]byte(project))
	return "g_" + s + "_" + hex.EncodeToString(sum[:3])
}

func randomPassword() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// provisionShared prepares a shared service for a project: with dedicated,
// its own database (or vhost) and user; otherwise the service's own login.
// It returns the values by role (host, porta, usuario, senha, banco, url).
func (s *Server) provisionShared(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	var req struct {
		Project   string `json:"project"`
		Dedicated *bool  `json:"dedicated"`
		// WriteEnv adds the connection to the project's .env (PG_HOST…),
		// with the password and URL in the keychain.
		WriteEnv bool `json:"writeEnv"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !labName.MatchString(req.Project) {
		writeErr(w, http.StatusBadRequest, errors.New("informe o nome do projeto"))
		return
	}
	dedicated := req.Dedicated == nil || *req.Dedicated
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	list, err := s.sharedServices(ctx, h)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	var sv *sharedSvc
	for i := range list {
		if list[i].ID == r.PathValue("id") || list[i].Name == r.PathValue("id") || strings.HasPrefix(list[i].ID, r.PathValue("id")) {
			sv = &list[i]
			break
		}
	}
	if sv == nil {
		writeErr(w, http.StatusNotFound, errors.New("serviço não encontrado"))
		return
	}
	if !sv.Running {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("o %s não está rodando; suba-o antes", sv.Name))
		return
	}
	if sv.Port == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("o %s não publica a porta %d na máquina; publique-a (ports: [\"%d:%d\"]) para outros projetos chegarem nele", sv.Name, sharedPorts[sv.Kind], sharedPorts[sv.Kind], sharedPorts[sv.Kind]))
		return
	}
	ins, err := h.Engine.Inspect(ctx, sv.ID)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	vals, use, err := provision(ctx, h.Engine, sv, ins, req.Project, dedicated)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	vals["host"], vals["porta"] = sharedHost, strconv.Itoa(sv.Port)
	vals["url"] = sharedURL(sv.Kind, vals)
	if err := s.recordShared(h.Machine(), sv.Name, req.Project, use); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resp := map[string]any{"service": sv.Name, "kind": sv.Kind, "values": vals, "dedicated": dedicated}
	if req.WriteEnv {
		names, err := s.writeSharedEnv(ctx, h, req.Project, sv.Kind, vals)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		resp["env"] = names
	}
	writeJSON(w, resp)
}

func sharedURL(kind string, v map[string]string) string {
	auth := ""
	if v["usuario"] != "" {
		auth = v["usuario"]
		if v["senha"] != "" {
			auth += ":" + v["senha"]
		}
		auth += "@"
	}
	hp := v["host"] + ":" + v["porta"]
	switch kind {
	case "postgres":
		return "postgres://" + auth + hp + "/" + v["banco"]
	case "mysql":
		return "mysql://" + auth + hp + "/" + v["banco"]
	case "redis":
		if v["senha"] != "" {
			return "redis://:" + v["senha"] + "@" + hp + "/" + v["banco"]
		}
		return "redis://" + hp + "/" + v["banco"]
	case "rabbitmq":
		return "amqp://" + auth + hp + "/" + v["banco"]
	case "mongo":
		return "mongodb://" + auth + hp + "/" + v["banco"] + "?authSource=" + v["banco"]
	}
	return ""
}

// runIn runs a shell script in the service's container; secrets go in env.
func runIn(ctx context.Context, eng *engine.Client, id, script string, env map[string]string) error {
	var ev []string
	for k, v := range env {
		ev = append(ev, k+"="+v)
	}
	out, code, err := eng.Exec(ctx, id, []string{"sh", "-c", script}, ev)
	if err != nil {
		return err
	}
	if code != 0 {
		msg := strings.TrimSpace(out)
		if len(msg) > 600 {
			msg = msg[len(msg)-600:]
		}
		return fmt.Errorf("o comando no serviço falhou (código %d): %s", code, msg)
	}
	return nil
}

const pgSQL = `SELECT format('CREATE ROLE %I LOGIN', :'u') WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'u')\gexec
ALTER ROLE :"u" WITH LOGIN PASSWORD :'pw';
SELECT format('CREATE DATABASE %I OWNER %I', :'db', :'u') WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = :'db')\gexec
GRANT ALL PRIVILEGES ON DATABASE :"db" TO :"u";
`

func provision(ctx context.Context, eng *engine.Client, sv *sharedSvc, ins engine.Inspect, project string, dedicated bool) (map[string]string, sharedUse, error) {
	id := sharedIdent(project)
	pw := randomPassword()
	switch sv.Kind {
	case "postgres":
		super := ins.EnvValue("POSTGRES_USER")
		if super == "" {
			super = "postgres"
		}
		superDB := ins.EnvValue("POSTGRES_DB")
		if superDB == "" {
			superDB = super
		}
		if !dedicated {
			return map[string]string{"usuario": super, "senha": ins.EnvValue("POSTGRES_PASSWORD"), "banco": superDB}, sharedUse{User: super, DB: superDB}, nil
		}
		err := runIn(ctx, eng, sv.ID, `export PGPASSWORD="${POSTGRES_PASSWORD:-}"; printf '%s\n' "$GANTRY_SQL" | psql -v ON_ERROR_STOP=1 -q -v u="$GANTRY_U" -v db="$GANTRY_DB" -v pw="$GANTRY_PW" -U "$GANTRY_SUPER" -d "$GANTRY_SUPERDB"`,
			map[string]string{"GANTRY_SQL": pgSQL, "GANTRY_U": id, "GANTRY_DB": id, "GANTRY_PW": pw, "GANTRY_SUPER": super, "GANTRY_SUPERDB": superDB})
		if err != nil {
			return nil, sharedUse{}, err
		}
		return map[string]string{"usuario": id, "senha": pw, "banco": id}, sharedUse{User: id, DB: id}, nil
	case "mysql":
		root := ins.EnvValue("MYSQL_ROOT_PASSWORD")
		if root == "" {
			root = ins.EnvValue("MARIADB_ROOT_PASSWORD")
		}
		if !dedicated {
			u := ins.EnvValue("MYSQL_USER")
			if u == "" {
				u = ins.EnvValue("MARIADB_USER")
			}
			p, db := ins.EnvValue("MYSQL_PASSWORD")+ins.EnvValue("MARIADB_PASSWORD"), ins.EnvValue("MYSQL_DATABASE")+ins.EnvValue("MARIADB_DATABASE")
			if u == "" {
				u, p = "root", root
			}
			return map[string]string{"usuario": u, "senha": p, "banco": db}, sharedUse{User: u, DB: db}, nil
		}
		sql := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`; CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s'; ALTER USER '%s'@'%%' IDENTIFIED BY '%s'; GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%'; FLUSH PRIVILEGES;", id, id, pw, id, pw, id, id)
		err := runIn(ctx, eng, sv.ID, `c=$(command -v mariadb || command -v mysql); export MYSQL_PWD="$GANTRY_ROOT"; printf '%s\n' "$GANTRY_SQL" | "$c" -uroot`,
			map[string]string{"GANTRY_SQL": sql, "GANTRY_ROOT": root})
		if err != nil {
			return nil, sharedUse{}, err
		}
		return map[string]string{"usuario": id, "senha": pw, "banco": id}, sharedUse{User: id, DB: id}, nil
	case "rabbitmq":
		if !dedicated {
			u := ins.EnvValue("RABBITMQ_DEFAULT_USER")
			if u == "" {
				u = "guest"
			}
			p := ins.EnvValue("RABBITMQ_DEFAULT_PASS")
			if p == "" && u == "guest" {
				p = "guest"
			}
			return map[string]string{"usuario": u, "senha": p, "banco": "/"}, sharedUse{User: u, DB: "/"}, nil
		}
		err := runIn(ctx, eng, sv.ID, `set -e
rabbitmqctl -q add_vhost "$GANTRY_U" 2>/dev/null || true
rabbitmqctl -q add_user "$GANTRY_U" "$GANTRY_PW" 2>/dev/null || rabbitmqctl -q change_password "$GANTRY_U" "$GANTRY_PW"
rabbitmqctl -q set_permissions -p "$GANTRY_U" "$GANTRY_U" ".*" ".*" ".*"`, map[string]string{"GANTRY_U": id, "GANTRY_PW": pw})
		if err != nil {
			return nil, sharedUse{}, err
		}
		return map[string]string{"usuario": id, "senha": pw, "banco": id}, sharedUse{User: id, DB: id}, nil
	case "mongo":
		ru, rp := ins.EnvValue("MONGO_INITDB_ROOT_USERNAME"), ins.EnvValue("MONGO_INITDB_ROOT_PASSWORD")
		if !dedicated {
			return map[string]string{"usuario": ru, "senha": rp, "banco": "admin"}, sharedUse{User: ru, DB: "admin"}, nil
		}
		js := `const d = db.getSiblingDB(process.env.GANTRY_U); if (d.getUser(process.env.GANTRY_U)) { d.updateUser(process.env.GANTRY_U, {pwd: process.env.GANTRY_PW}) } else { d.createUser({user: process.env.GANTRY_U, pwd: process.env.GANTRY_PW, roles: [{role: "dbOwner", db: process.env.GANTRY_U}]}) }`
		err := runIn(ctx, eng, sv.ID, `c=$(command -v mongosh || command -v mongo); if [ -n "$GANTRY_RU" ]; then "$c" --quiet -u "$GANTRY_RU" -p "$GANTRY_RP" --authenticationDatabase admin --eval "$GANTRY_JS"; else "$c" --quiet --eval "$GANTRY_JS"; fi`,
			map[string]string{"GANTRY_U": id, "GANTRY_PW": pw, "GANTRY_RU": ru, "GANTRY_RP": rp, "GANTRY_JS": js})
		if err != nil {
			return nil, sharedUse{}, err
		}
		return map[string]string{"usuario": id, "senha": pw, "banco": id}, sharedUse{User: id, DB: id}, nil
	case "redis":
		// Redis has no users per database here: the project gets the next
		// free database number, with the service's password.
		p := ins.EnvValue("REDIS_PASSWORD")
		for i, a := range ins.Config.Cmd {
			if a == "--requirepass" && i+1 < len(ins.Config.Cmd) {
				p = ins.Config.Cmd[i+1]
			}
		}
		return map[string]string{"usuario": "", "senha": p, "banco": "0"}, sharedUse{DB: "0"}, nil
	}
	return nil, sharedUse{}, fmt.Errorf("serviço %s sem suporte", sv.Kind)
}

// sharedClient opens a terminal with the service's own client (psql,
// mysql, redis-cli, mongosh), logged in as the project's user when the
// project has one there, otherwise as the service's admin.
func (s *Server) sharedClient(w http.ResponseWriter, r *http.Request, h *hosts.Host) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := s.sharedServices(ctx, h)
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	var sv *sharedSvc
	for i := range list {
		if list[i].ID == r.PathValue("id") || list[i].Name == r.PathValue("id") {
			sv = &list[i]
		}
	}
	if sv == nil || !sv.Running {
		writeErr(w, http.StatusNotFound, errors.New("serviço não encontrado ou parado"))
		return
	}
	use := sv.Consumers[r.URL.Query().Get("project")]
	user, db := use.User, use.DB
	dbArg := ""
	if db != "" {
		dbArg = " " + shellQuote(db)
	}
	var line string
	switch sv.Kind {
	case "postgres":
		if user == "" {
			line = `exec psql -U "${POSTGRES_USER:-postgres}" -d "${POSTGRES_DB:-${POSTGRES_USER:-postgres}}"`
		} else {
			line = "exec psql -U " + shellQuote(user) + " -d " + shellQuote(db)
		}
	case "mysql":
		line = `c=$(command -v mariadb || command -v mysql); MYSQL_PWD="${MYSQL_ROOT_PASSWORD:-$MARIADB_ROOT_PASSWORD}" exec "$c" -uroot` + dbArg
	case "redis":
		line = `if [ -n "$REDIS_PASSWORD" ]; then REDISCLI_AUTH="$REDIS_PASSWORD" exec redis-cli; else exec redis-cli; fi`
	case "mongo":
		line = `c=$(command -v mongosh || command -v mongo); if [ -n "$MONGO_INITDB_ROOT_USERNAME" ]; then exec "$c" -u "$MONGO_INITDB_ROOT_USERNAME" -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin` + dbArg + `; else exec "$c"` + dbArg + `; fi`
	case "rabbitmq":
		line = `echo "Filas do vhost "` + shellQuote(firstNonEmpty(db, "/")) + `":"; rabbitmqctl list_queues -p ` + shellQuote(firstNonEmpty(db, "/")) + ` name messages consumers; exec sh`
	default:
		writeErr(w, http.StatusBadRequest, errors.New("serviço sem cliente"))
		return
	}
	ex, err := h.Engine.ExecTTY(ctx, sv.ID, []string{"sh", "-c", line})
	if err != nil {
		writeErr(w, engineStatus(err), err)
		return
	}
	writeJSON(w, map[string]string{"id": s.addTerm(ex)})
}

func firstNonEmpty(a ...string) string {
	for _, x := range a {
		if x != "" {
			return x
		}
	}
	return ""
}

var sharedPrefix = map[string]string{"postgres": "PG", "mysql": "MYSQL", "redis": "REDIS", "rabbitmq": "RABBIT", "mongo": "MONGO"}

// writeSharedEnv puts a shared service's connection in a project's .env
// (PG_HOST, PG_PORTA, PG_USUARIO, PG_BANCO; PG_SENHA and PG_URL go to the
// keychain), replacing earlier values.
func (s *Server) writeSharedEnv(ctx context.Context, h *hosts.Host, lab, kind string, vals map[string]string) ([]string, error) {
	l, err := s.findLab(ctx, h, lab)
	if err != nil || l.WorkingDir == "" {
		return nil, errors.New("o projeto não tem pasta para o .env")
	}
	p := sharedPrefix[kind]
	set := map[string]string{}
	var names []string
	for _, role := range []string{"host", "porta", "usuario", "senha", "banco", "url"} {
		if v := vals[role]; v != "" {
			k := p + "_" + strings.ToUpper(role)
			set[k] = v
			names = append(names, k)
		}
	}
	envPath := path.Join(l.WorkingDir, ".env")
	b, err := readEnvFile(ctx, h, envPath)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, ln := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if m := envLine.FindStringSubmatch(ln); m != nil {
			if _, ok := set[m[1]]; ok {
				continue
			}
		}
		if strings.HasPrefix(ln, "# ") && strings.HasSuffix(ln, ": no chaveiro do Gantry") {
			if _, ok := set[strings.TrimSuffix(strings.TrimPrefix(ln, "# "), ": no chaveiro do Gantry")]; ok {
				continue
			}
		}
		if ln != "" || len(lines) > 0 {
			lines = append(lines, ln)
		}
	}
	for _, k := range names {
		lines = append(lines, k+"="+envQuote(set[k]))
	}
	if err := h.WriteFile(ctx, envPath, []byte(strings.Join(lines, "\n")+"\n")); err != nil {
		return nil, err
	}
	_, err = s.moveSecrets(ctx, h, lab, l.WorkingDir, []string{p + "_SENHA", p + "_URL"})
	return names, err
}
