package cvm

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
	"regexp"
	"sort"
	"strings"
	"time"
)

// Snapshots of the data disk: clones of dados.raw made by APFS (cp -c), so
// they are instant and take only what changes afterwards. With the machine
// on, it syncs and pauses for the clone.

const fSnaps = "snapshots"

// Snap is one snapshot.
type Snap struct {
	ID     string `json:"id"`
	At     int64  `json:"at"`
	Reason string `json:"reason"`
	Auto   bool   `json:"auto"`
	// Extra is what the clone takes on the Mac (blocks no longer shared
	// with the disk); 0 while it is the same.
	Extra int64 `json:"extra"`
}

var snapID = regexp.MustCompile(`^\d{8}-\d{6}$`)

func snapDir(name string) string { return filepath.Join(Dir(name), fSnaps) }

// TakeSnapshot clones the data disk now.
func TakeSnapshot(ctx context.Context, name, reason string, auto bool, log io.Writer) (Snap, error) {
	if err := os.MkdirAll(snapDir(name), 0o700); err != nil {
		return Snap{}, err
	}
	now := time.Now()
	sn := Snap{ID: now.Format("20060102-150405"), At: now.Unix(), Reason: strings.TrimSpace(reason), Auto: auto}
	if sn.Reason == "" {
		sn.Reason = "manual"
	}
	dst := filepath.Join(snapDir(name), sn.ID+".raw")
	running := pid(name) != 0
	if running {
		// What the guest has in memory goes to the disk, then it holds still.
		_, _ = Run(ctx, name, "sync")
		if _, err := rest(ctx, name, http.MethodPost, `{"state":"Pause"}`); err != nil {
			return Snap{}, fmt.Errorf("não consegui pausar a máquina para o snapshot: %v", err)
		}
		defer func() { _, _ = rest(context.Background(), name, http.MethodPost, `{"state":"Resume"}`) }()
	}
	out, err := exec.CommandContext(ctx, "cp", "-c", filepath.Join(Dir(name), fData), dst).CombinedOutput()
	if err != nil {
		_ = os.Remove(dst)
		return Snap{}, fmt.Errorf("cp -c: %s %v", strings.TrimSpace(string(out)), err)
	}
	b, _ := json.Marshal(sn)
	if err := os.WriteFile(filepath.Join(snapDir(name), sn.ID+".json"), b, 0o600); err != nil {
		return Snap{}, err
	}
	if log != nil {
		fmt.Fprintf(log, "Snapshot %s (%s)\n", sn.ID, sn.Reason)
	}
	return sn, nil
}

// Snapshots lists them, newest first.
func Snapshots(name string) []Snap {
	ents, _ := os.ReadDir(snapDir(name))
	out := []Snap{}
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !snapID.MatchString(id) {
			continue
		}
		var sn Snap
		b, err := os.ReadFile(filepath.Join(snapDir(name), e.Name()))
		if err != nil || json.Unmarshal(b, &sn) != nil {
			continue
		}
		if fi, err := os.Stat(filepath.Join(snapDir(name), id+".raw")); err == nil {
			sn.Extra = occupied(fi)
		} else {
			continue
		}
		out = append(out, sn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// RestoreSnapshot puts a snapshot back as the data disk (machine off). The
// disk it replaces is kept as one more snapshot, so nothing is lost.
func RestoreSnapshot(ctx context.Context, name, id string, log io.Writer) error {
	if !snapID.MatchString(id) {
		return errors.New("snapshot inválido")
	}
	if pid(name) != 0 {
		return errors.New("desligue a máquina antes de restaurar")
	}
	src := filepath.Join(snapDir(name), id+".raw")
	if _, err := os.Stat(src); err != nil {
		return errors.New("snapshot não encontrado")
	}
	if _, err := TakeSnapshot(ctx, name, "antes de restaurar "+id, true, log); err != nil {
		return err
	}
	data := filepath.Join(Dir(name), fData)
	tmp := data + ".restaurando"
	_ = os.Remove(tmp)
	if out, err := exec.CommandContext(ctx, "cp", "-c", src, tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("cp -c: %s %v", strings.TrimSpace(string(out)), err)
	}
	if err := os.Rename(tmp, data); err != nil {
		return err
	}
	fmt.Fprintf(log, "Disco de dados restaurado do snapshot %s.\n", id)
	return nil
}

// DeleteSnapshot removes one.
func DeleteSnapshot(name, id string) error {
	if !snapID.MatchString(id) {
		return errors.New("snapshot inválido")
	}
	_ = os.Remove(filepath.Join(snapDir(name), id+".raw"))
	return os.Remove(filepath.Join(snapDir(name), id+".json"))
}

// PruneSnapshots keeps the newest keep automatic snapshots (manual ones
// stay until the user deletes them).
func PruneSnapshots(name string, keep int) {
	if keep <= 0 {
		keep = 7
	}
	n := 0
	for _, sn := range Snapshots(name) {
		if !sn.Auto {
			continue
		}
		n++
		if n > keep {
			_ = DeleteSnapshot(name, sn.ID)
		}
	}
}

// DailySnapshot takes the day's automatic snapshot if the spec asks and
// there is none today.
func DailySnapshot(ctx context.Context, name string) {
	s, err := Load(name)
	if err != nil || !s.SnapDaily {
		return
	}
	today := time.Now().Format("20060102")
	for _, sn := range Snapshots(name) {
		if sn.Auto && strings.HasPrefix(sn.ID, today) {
			return
		}
	}
	if _, err := TakeSnapshot(ctx, name, "diário, ao ligar", true, nil); err == nil {
		PruneSnapshots(name, s.SnapKeep)
	}
}
