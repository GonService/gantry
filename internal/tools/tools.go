// Package tools tells which of the programs Gantry uses are installed on a
// machine (this computer or a server), what each one is for and how to
// install the missing ones there.
package tools

import (
	_ "embed"
	"strings"
)

//go:embed tools.sh
var Script string

// Tool is one thing Gantry uses on a machine.
type Tool struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Area  string `json:"area"` // Containers, VMs, Kubernetes, Geral
	Why   string `json:"why"`
	Need  string `json:"need"` // essencial, recomendado, opcional
	Have  bool   `json:"have"`
	Found string `json:"found,omitempty"` // what was found (program, path)
	// Install is the command that installs it on this machine ("" when
	// there is none to suggest); Note explains anything else.
	Install string `json:"install,omitempty"`
	Note    string `json:"note,omitempty"`
	// Brew: Install runs without sudo (Homebrew), so the app can run it on
	// this computer.
	Brew bool `json:"brew,omitempty"`
}

// Report is the tools of one machine.
type Report struct {
	OS    string `json:"os"`
	Arch  string `json:"arch"`
	PM    string `json:"pm,omitempty"` // package manager found
	Local bool   `json:"local"`
	Tools []Tool `json:"tools"`
	// Missing counts the essential and recommended tools that are missing.
	Missing int `json:"missing"`
}

// Has says whether the tool with this id is there.
func (r Report) Has(id string) bool {
	for _, t := range r.Tools {
		if t.ID == id {
			return t.Have
		}
	}
	return false
}

// Parse reads the output of Script. local is this computer (the tools only
// it needs, like ssh and the file dialog, show only there).
func Parse(out string, local bool) Report {
	kv := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			kv[k] = v
		}
	}
	r := Report{OS: kv["os"], Arch: kv["arch"], PM: kv["pm"], Local: local}
	mac := r.OS == "Darwin"
	linux := r.OS == "Linux"
	pkg := func(brew, apt, dnf, pacman, apk, zypper string) string {
		switch r.PM {
		case "brew":
			if brew != "" {
				return "brew install " + brew
			}
		case "apt-get":
			if apt != "" {
				return "sudo apt install -y " + apt
			}
		case "dnf", "yum":
			if dnf != "" {
				return "sudo " + r.PM + " install -y " + dnf
			}
		case "pacman":
			if pacman != "" {
				return "sudo pacman -S --needed " + pacman
			}
		case "apk":
			if apk != "" {
				return "sudo apk add " + apk
			}
		case "zypper":
			if zypper != "" {
				return "sudo zypper install -y " + zypper
			}
		}
		return ""
	}
	first := func(ids ...string) string {
		for _, id := range ids {
			if kv[id] != "" {
				return id
			}
		}
		return ""
	}
	add := func(t Tool) {
		t.Brew = strings.HasPrefix(t.Install, "brew ")
		if t.Have {
			t.Install = ""
		}
		r.Tools = append(r.Tools, t)
	}

	// Containers
	engines := []string{}
	for _, e := range []string{"docker", "podman", "nerdctl"} {
		if kv[e] != "" {
			engines = append(engines, e)
		}
	}
	eng := Tool{ID: "engine", Name: "Engine de containers", Area: "Containers", Need: "essencial",
		Why: "Docker, Podman ou containerd (nerdctl): é quem roda os containers.", Have: len(engines) > 0, Found: strings.Join(engines, ", ")}
	if mac {
		eng.Install = "brew install podman docker-compose && podman machine init --now"
		eng.Note = "Ou o OrbStack ou o Docker Desktop. A tela Esta máquina instala e inicia o Podman."
	} else {
		eng.Install = pkg("podman", "podman", "podman", "podman", "podman", "podman")
	}
	add(eng)
	compose := []string{}
	if kv["docker-compose-plugin"] != "" {
		compose = append(compose, "docker compose")
	}
	for _, c := range []string{"docker-compose", "podman-compose"} {
		if kv[c] != "" {
			compose = append(compose, c)
		}
	}
	if kv["podman-compose-wrapper"] != "" && len(compose) == 0 {
		compose = append(compose, "podman compose")
	}
	cmp := Tool{ID: "compose", Name: "Compose", Area: "Containers", Need: "essencial",
		Why: "Sobe os projetos (compose.yaml). Sem ele, só os containers avulsos funcionam.", Have: len(compose) > 0, Found: strings.Join(compose, ", ")}
	switch {
	case mac:
		cmp.Install = "brew install docker-compose"
	case kv["podman"] != "" && kv["docker"] == "":
		cmp.Install = pkg("podman-compose", "podman-compose", "podman-compose", "podman-compose", "podman-compose", "podman-compose")
	case kv["docker-repo"] != "":
		// Docker CE from download.docker.com: same package name everywhere.
		cmp.Install = pkg("", "docker-compose-plugin", "docker-compose-plugin", "", "", "docker-compose-plugin")
	default:
		// The distribution's Docker (moby-engine, docker.io): its own package.
		cmp.Install = pkg("docker-compose", "docker-compose-v2", "docker-compose", "docker-compose", "docker-cli-compose", "docker-compose")
		cmp.Note = "Com o Docker do repositório oficial (download.docker.com), o pacote é docker-compose-plugin."
	}
	add(cmp)

	// VMs
	qemu := Tool{ID: "qemu", Name: "QEMU", Area: "VMs", Need: "recomendado",
		Why: "Roda as máquinas virtuais dos projetos (x-vms).", Have: kv["qemu-system"] != "" && kv["qemu-img"] != "",
		Found: strings.TrimSpace(baseName(kv["qemu-system"]) + " " + baseName(kv["qemu-img"]))}
	qemu.Install = pkg("qemu", "qemu-system qemu-utils", "qemu-kvm qemu-img", "qemu-full", "qemu-system-"+qemuArch(r.Arch)+" qemu-img", "qemu-kvm qemu-tools")
	if kv["qemu-system"] != "" && kv["qemu-img"] == "" {
		qemu.Note = "Falta só o qemu-img."
	}
	add(qemu)
	if !mac {
		iso := Tool{ID: "iso", Name: "Gerador de ISO", Area: "VMs", Need: "recomendado",
			Why:   "Monta o disco de configuração (cloud-init) das VMs: usuário, chave SSH, pacotes.",
			Found: first("genisoimage", "mkisofs", "xorriso")}
		iso.Have = iso.Found != ""
		iso.Install = pkg("", "genisoimage", "genisoimage", "cdrtools", "cdrkit", "genisoimage")
		add(iso)
	}
	dl := Tool{ID: "download", Name: "curl ou wget", Area: "VMs", Need: "recomendado",
		Why: "Baixa as imagens das VMs (Debian, Ubuntu…).", Found: first("curl", "wget")}
	dl.Have = dl.Found != ""
	dl.Install = pkg("curl", "curl", "curl", "curl", "curl", "curl")
	add(dl)
	if linux {
		acc := Tool{ID: "kvm", Name: "Aceleração (KVM)", Area: "VMs", Need: "recomendado",
			Why: "Sem ela as VMs rodam emuladas, muitas vezes mais lentas.", Have: kv["kvm"] != ""}
		if acc.Have {
			acc.Found = "/dev/kvm"
		} else if kv["kvm-exists"] != "" {
			acc.Install = "sudo usermod -aG kvm \"$USER\""
			acc.Note = "O /dev/kvm existe, mas este usuário não pode usá-lo. Depois do comando, entre de novo na sessão."
		} else {
			acc.Note = "Esta máquina não oferece virtualização (KVM). Num servidor na nuvem ou numa VM, ative a virtualização aninhada."
		}
		add(acc)
		br := Tool{ID: "bridge", Name: "qemu-bridge-helper", Area: "VMs", Need: "opcional",
			Why: "Só para a rede do projeto (lan: true), que põe as VMs na rede da máquina.", Have: kv["bridge-helper"] != "", Found: kv["bridge-helper"]}
		br.Install = pkg("", "qemu-system-common", "qemu-common", "qemu-full", "qemu-bridge-helper", "qemu")
		add(br)
	}
	if mac {
		vm := Tool{ID: "socket_vmnet", Name: "socket_vmnet", Area: "VMs", Need: "opcional",
			Why: "Só para a rede do projeto (lan: true), que põe as VMs na rede do Mac.", Have: kv["socket_vmnet"] != "", Found: kv["socket_vmnet"]}
		vm.Install = "brew install socket_vmnet"
		vm.Note = "Depois: sudo brew services start socket_vmnet"
		add(vm)
	}

	// Kubernetes (the clusters are used from this computer)
	if local {
		helm := Tool{ID: "helm", Name: "Helm", Area: "Kubernetes", Need: "opcional",
			Why:  "Só para voltar uma release do Helm a uma versão anterior. O resto dos clusters funciona sem nada instalado (nem kubectl).",
			Have: kv["helm"] != "", Found: kv["helm"]}
		helm.Install = pkg("helm", "", "helm", "helm", "helm", "helm")
		if helm.Install == "" {
			helm.Install = "sudo snap install helm --classic"
		}
		add(helm)
	}

	// Labs de rede: IOL, Dynamips and VPCS run as programs on the machine
	// under a small python3 runner (console, startup configuration).
	if linux || mac {
		py := Tool{ID: "python3", Name: "python3", Area: "Labs de rede", Need: "recomendado",
			Why: "Roda os equipamentos de rede dos labs (IOL, Dynamips e VPCS): console e configuração inicial.", Have: kv["python3"] != "", Found: kv["python3"]}
		py.Install = pkg("python3", "python3", "python3", "python", "python3", "python3")
		add(py)
		dyn := Tool{ID: "dynamips", Name: "Dynamips", Area: "Labs de rede", Need: "opcional",
			Why: "Roda os roteadores Cisco clássicos (7200, 3725, 2691) dos labs, como no PNETLab e no GNS3.", Have: kv["dynamips"] != "", Found: kv["dynamips"]}
		dyn.Install = pkg("dynamips", "dynamips", "dynamips", "", "", "dynamips")
		if dyn.Install == "" && !dyn.Have {
			dyn.Note = "Sem pacote neste sistema: compile do código do GNS3 (github.com/GNS3/dynamips)."
		}
		add(dyn)
		if _, ok := kv["lib32"]; ok {
			l32 := Tool{ID: "lib32", Name: "Bibliotecas de 32 bits", Area: "Labs de rede", Need: "opcional",
				Why: "As imagens IOL mais antigas (i86bi_linux…) são de 32 bits e precisam delas para rodar.", Have: kv["lib32"] != ""}
			if l32.Have {
				l32.Found = "/lib/ld-linux.so.2"
			}
			switch r.PM {
			case "apt-get":
				l32.Install = "sudo dpkg --add-architecture i386 && sudo apt update && sudo apt install -y libc6:i386 libstdc++6:i386 libssl3:i386"
			case "dnf", "yum":
				// The i686 packages must match the x86_64 versions exactly; an
				// older x86_64 libstdc++ makes the whole transaction fail.
				l32.Install = "sudo " + r.PM + " upgrade -y --refresh glibc libgcc libstdc++ && sudo " + r.PM + " install -y glibc.i686 libstdc++.i686 openssl-libs.i686"
			default:
				l32.Install = pkg("", "", "glibc.i686 libstdc++.i686 openssl-libs.i686", "lib32-glibc lib32-gcc-libs lib32-openssl", "", "glibc-32bit libstdc++6-32bit libopenssl3-32bit")
			}
			add(l32)
		}
	}

	// Geral
	git := Tool{ID: "git", Name: "git", Area: "Geral", Need: "opcional",
		Why: "Para criar projetos a partir de um repositório e ver commits novos.", Have: kv["git"] != "", Found: kv["git"]}
	git.Install = pkg("git", "git", "git", "git", "git", "git")
	if mac && git.Install == "" {
		git.Install = "xcode-select --install"
	}
	add(git)
	if local {
		ssh := Tool{ID: "ssh", Name: "ssh", Area: "Geral", Need: "recomendado",
			Why: "Conecta nos servidores (usa o seu ~/.ssh/config e as suas chaves).", Have: kv["ssh"] != "", Found: kv["ssh"]}
		ssh.Install = pkg("", "openssh-client", "openssh-clients", "openssh", "openssh-client", "openssh")
		add(ssh)
		if linux {
			pick := Tool{ID: "dialogo", Name: "zenity ou kdialog", Area: "Geral", Need: "opcional",
				Why: "Abre a janela do sistema para escolher pastas e arquivos; sem ele, o caminho é digitado.", Found: first("zenity", "kdialog")}
			pick.Have = pick.Found != ""
			pick.Install = pkg("", "zenity", "zenity", "zenity", "zenity", "zenity")
			add(pick)
		}
	}
	for _, t := range r.Tools {
		if !t.Have && t.Need != "opcional" {
			r.Missing++
		}
	}
	return r
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func qemuArch(a string) string {
	if a == "arm64" || a == "aarch64" {
		return "aarch64"
	}
	return "x86_64"
}
