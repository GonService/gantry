package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Native file dialogs, so the user picks where things are saved and what
// is opened (backups, exports), on the computer running the app.

type pickReq struct {
	Kind  string `json:"kind"` // pasta, salvar, abrir
	Title string `json:"title"`
	Dir   string `json:"dir"`  // where the dialog starts
	Name  string `json:"name"` // suggested file name (salvar)
	Ext   string `json:"ext"`  // file type to open, e.g. "gz"
}

var errCanceled = errors.New("cancelado")

// pickerCmd lets tests replace the dialog.
var pickerCmd = runPicker

func (s *Server) pick(w http.ResponseWriter, r *http.Request) {
	var req pickReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Dir == "" {
		home, _ := os.UserHomeDir()
		req.Dir = home
	}
	if strings.HasPrefix(req.Dir, "~/") {
		home, _ := os.UserHomeDir()
		req.Dir = filepath.Join(home, req.Dir[2:])
	}
	_ = os.MkdirAll(req.Dir, 0o755)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	p, err := pickerCmd(ctx, req)
	if errors.Is(err, errCanceled) {
		writeJSON(w, map[string]any{"canceled": true})
		return
	}
	if err != nil {
		writeErr(w, http.StatusNotImplemented, err)
		return
	}
	writeJSON(w, map[string]string{"path": p})
}

func asQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func runPicker(ctx context.Context, q pickReq) (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// Asked to the frontmost app, so the dialog shows over the window.
		loc := " default location (POSIX file " + asQuote(q.Dir) + ")"
		var expr string
		switch q.Kind {
		case "pasta":
			expr = "choose folder with prompt " + asQuote(q.Title) + loc
		case "salvar":
			expr = "choose file name with prompt " + asQuote(q.Title) + " default name " + asQuote(q.Name) + loc
		default:
			expr = "choose file with prompt " + asQuote(q.Title) + loc
		}
		script := "tell application (path to frontmost application as text)\nset p to " + expr + "\nend tell\nPOSIX path of p"
		cmd = exec.CommandContext(ctx, "osascript", "-e", script)
	case "windows":
		var ps string
		switch q.Kind {
		case "pasta":
			ps = `$d=New-Object System.Windows.Forms.FolderBrowserDialog; $d.Description=$env:G_TITLE; $d.SelectedPath=$env:G_DIR; if($d.ShowDialog() -eq 'OK'){$d.SelectedPath}else{exit 3}`
		case "salvar":
			ps = `$d=New-Object System.Windows.Forms.SaveFileDialog; $d.Title=$env:G_TITLE; $d.InitialDirectory=$env:G_DIR; $d.FileName=$env:G_NAME; if($d.ShowDialog() -eq 'OK'){$d.FileName}else{exit 3}`
		default:
			ps = `$d=New-Object System.Windows.Forms.OpenFileDialog; $d.Title=$env:G_TITLE; $d.InitialDirectory=$env:G_DIR; if($d.ShowDialog() -eq 'OK'){$d.FileName}else{exit 3}`
		}
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-STA", "-Command", "Add-Type -AssemblyName System.Windows.Forms; "+ps)
		cmd.Env = append(os.Environ(), "G_TITLE="+q.Title, "G_DIR="+q.Dir, "G_NAME="+q.Name)
	default:
		if p, err := exec.LookPath("zenity"); err == nil {
			args := []string{"--file-selection", "--title=" + q.Title}
			switch q.Kind {
			case "pasta":
				args = append(args, "--directory", "--filename="+q.Dir+"/")
			case "salvar":
				args = append(args, "--save", "--confirm-overwrite", "--filename="+filepath.Join(q.Dir, q.Name))
			default:
				args = append(args, "--filename="+q.Dir+"/")
			}
			cmd = exec.CommandContext(ctx, p, args...)
		} else if p, err := exec.LookPath("kdialog"); err == nil {
			switch q.Kind {
			case "pasta":
				cmd = exec.CommandContext(ctx, p, "--title", q.Title, "--getexistingdirectory", q.Dir)
			case "salvar":
				cmd = exec.CommandContext(ctx, p, "--title", q.Title, "--getsavefilename", filepath.Join(q.Dir, q.Name))
			default:
				cmd = exec.CommandContext(ctx, p, "--title", q.Title, "--getopenfilename", q.Dir)
			}
		} else {
			return "", errors.New("sem seletor de arquivos nesta máquina (instale o zenity); digite o caminho")
		}
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg := string(ee.Stderr)
			// osascript -128 (Cancel), zenity/kdialog 1, our PowerShell 3.
			if strings.Contains(msg, "-128") || (runtime.GOOS != "darwin" && (ee.ExitCode() == 1 || ee.ExitCode() == 3)) {
				return "", errCanceled
			}
			return "", errors.New(strings.TrimSpace(msg))
		}
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", errCanceled
	}
	if q.Kind == "pasta" {
		p = strings.TrimSuffix(p, "/")
		if p == "" {
			p = "/"
		}
	}
	return p, nil
}

func filepathToSlash(p string) string { return filepath.ToSlash(p) }

// openLocal opens a file the user picked on this computer.
func openLocal(p string) (*os.File, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("não consegui abrir %s: %v", p, err)
	}
	if st, err := f.Stat(); err != nil || st.IsDir() {
		f.Close()
		return nil, fmt.Errorf("%s não é um arquivo", p)
	}
	return f, nil
}
