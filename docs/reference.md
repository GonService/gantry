# Gantry: reference

**English** · [Português](referencia.md) · [Step-by-step guide](guide/README.md) · [Back to the start](../README.md)

A desktop app to manage local and remote machines and what runs on them: containers (Docker, Podman, containerd), QEMU VMs and the projects that combine both. It is a single binary.

Gantry talks straight to the Docker or Podman API, with nothing hosted and no agent required on servers. The state comes from the engine itself, so what you create from the terminal (`docker run`, `docker compose up`, `podman compose up`) appears in the interface right away. Projects are your `compose.yaml` files; the app does not invent another format, only two extensions (`x-gantry` and `x-vms`).

The interface is available in English and Portuguese (gear › General). Some names in this reference (compose keys such as `depois:`, `usa:` and `x-redes`, file names such as `IMPORTACAO.md`, CLI flags such as `-expor-na-rede`) are Portuguese in the app itself and are written here as they are.

## What it does today

- **Machines**
  - The sidebar lists machines (this one and the servers), not containers. A server with Docker, Podman and containerd appears once.
  - **Folders and subfolders** organize the sidebar (by client, environment…). A folder shows how many machines it has and how many have a problem, even closed; its 🔍 filters only inside it; large folders open summarized (problems first) with "see all". The search at the top looks in all of them and shows the folder of each result.
  - The **gear** next to the version opens Settings: **Machines** (add, edit, remove, choose the folder), **Folders** (create, rename, move, delete; only empty folders), **Credentials** (image registries, SSH passwords of this session, where secrets are kept) and **General** (theme, language, version).
  - Remote servers connect only when you open them: the app does not open SSH to all of them at startup. **Disconnect** closes the connection.
  - The machine page shows the hardware and the CPU, memory and disk usage, read by a short script (local or over SSH, no agent). Then the tabs:
    - **Projects** (the first tab), newest first, with **Create project**;
    - **Containers**, with one sub-tab per engine (Docker, Podman, containerd…) and, in each, Containers, Images, Volumes and Networks;
    - **VMs** of all projects, with screen (VNC), console, SSH, start, stop and remove;
    - **Services**: databases and queues other projects can use (see Shared services);
    - **Settings** (connection, other engines, observability, agent) and, on this machine, **Engines** (install, start and stop).
  - Long lists have a filter and pages of 10, 25, 50 or 100. Everything is clickable.
- **Kubernetes clusters** (what Lens does, inside Gantry)
  - Clusters appear in the sidebar, in folders, next to the machines. External ones come in through the gear › **Clusters** (a context of `~/.kube/config` or a pasted kubeconfig); the ones created by Gantry projects come in by themselves.
  - The cluster page has an Overview (nodes with CPU and memory, failing pods and why, recent warnings), the lists of workloads, network, configuration and storage, nodes, namespaces, events and Helm releases, with a namespace filter.
  - Clicking an item opens its details on the side, as in Lens. For a pod: state, node, IPs, owner, labels and, per container, image, state, restarts, ports, command, variables (the ones from a Secret appear only as a reference), mounts, resources and usage, probes, with Logs and Shell for each container; plus volumes and events. Deployments, Jobs, CronJobs, Services and nodes show their pods or jobs; ConfigMaps show their data; Secrets show only their keys.
  - The cluster menu has groups that open and close (the app remembers).
  - Lists sort by any column: click the header (again to reverse). By default by name; events, newest first. The app remembers the order of each kind.
  - Custom resources (CRDs: cert-manager Certificates, Argo CD Applications…) appear in the menu under **Custom**, with the columns the CRD author defined and the detail with conditions, spec and status. If your account cannot list CRDs, the kinds come from API discovery, without those columns.
  - **Port forward** on a pod or Service opens a port on this machine (127.0.0.1) that leads to it, like `kubectl port-forward` (to connect DBeaver to a Postgres in the cluster, for example); open ports are listed in **Forwarded ports**, at the top of the cluster.
  - Logs of the **previous run** of a container (the one that died in a CrashLoopBackOff); on a pod in that state, Logs opens there.
  - Live logs do not stop by themselves: when the cluster (or a load balancer, a proxy, log file rotation) closes the connection, Gantry reconnects from the last line, without repeating any. If the container restarts, the log follows the new run with a notice; it ends only when the pod finishes or is deleted.
  - Deployments show the **revision history** with **Roll back to this one** (`kubectl rollout undo`); nodes can be **drained** (cordon and evict the pods, respecting PodDisruptionBudgets).
  - **Helm** releases open with history, values, notes and manifest; **Roll back to this one** runs the `helm` of this machine.
  - CPU and memory charts of the last hour for pods, workloads and nodes (Gantry records usage every 15 s while the cluster is open; needs metrics-server).
  - Gantry asks the cluster what your account can do and hides the buttons it cannot use.
  - In large clusters, lists are read once, in pages, then followed by watch, instead of read whole at every refresh.
  - Actions: pod logs and shell, scale and restart deployments, run and suspend CronJobs, cordon nodes, see and edit YAML, delete, see Secret values, **Apply YAML…** and **Open** a Service in the browser.
  - **Diagnostics** on a failing pod: what is wrong now (not ready, failing probes since the current start, restarts) and since when.
- **Create project** (a three-step wizard; every project ends as a compose in Gantry's format)
  1. What the project has: **containers**, a **virtual machine** or **VM + containers**.
  2. Containers: the engine (Docker, Podman, containerd) and the source: a **template**, a **pasted docker-compose** (or opened from a file) or **`docker run`, `podman run` or `nerdctl run` commands**, converted to compose (ports, volumes, variables, network, limits, restart, healthcheck…; what has no equivalent becomes a warning).
     VM: a **template** (Debian, Ubuntu with Docker, k3s, Rocky, install from ISO…) or **your own setup**: system (Debian, Ubuntu, Rocky, Alma, NixOS, a qcow2 image of yours or an **ISO**), CPUs, memory, disk, ports, packages, **startup script** with ready examples (Docker, Podman, Nginx, k3s, time zone, user with password) and advanced cloud-init.
  3. Review: the editable YAML with a summary on the side; variables become fields (and go to `.env`), name and folder. By default the project is also **saved as a template**, to generate the same again.
  - Under **Other ways**: single container (the full form), project from a git repository, open a folder with a compose, import a package, import from PNETLab/EVE-NG.
  - ISO VMs start with an empty disk and boot the installer; **Screen** opens VNC (with a password, through the SSH tunnel on servers) in the system viewer.
- **Templates** (like Portainer's Custom Templates, without a menu of their own: they appear in Create project)
  - A template is a `compose.yaml` with containers on any engine (`x-gantry`) and VMs (`x-vms`). Variables like `${SENHA:?informe}` and `${PORTA:-8080}` become fields when generating the project; the values go to `.env`.
  - Gantry's own: Blank, Nginx, Postgres, Postgres + Adminer, MySQL, MariaDB, MongoDB, Redis, RabbitMQ, Kafka + Kafka UI, MinIO, Prometheus + Grafana, Uptime Kuma, Vault, Keycloak, Gitea, n8n, Mailpit, WordPress, VS Code in the browser, Jupyter, Ollama + Open WebUI, Paperclip (uses an existing Postgres), Web + database, the VMs Debian 12 and 13, Ubuntu with Docker, k3s, Rocky, Nginx, PostgreSQL, RabbitMQ, Podman, LAMP, NixOS and from ISO, the k3s cluster (3 VMs), and Cisco network labs (switches, routers, campus, IOL, Dynamips).
  - **Observability lab** (VM + containers): Traefik on port 80 leading to the Nginx of a Debian VM (any device on the network reaches it through the machine's IP), with Prometheus, Grafana, node_exporter and the Nginx exporter in containers (Podman by default). The Machines, Prometheus, Nginx and Traefik dashboards come provisioned in Grafana. The Traefik dashboard is on :8090. On a Mac, the containerized node_exporter measures the Podman VM (`podman machine`); to measure macOS itself, run `brew install node_exporter` and uncomment the `mac` target in `prometheus.yml`.
  - Templates can bring files along with the compose (configs, dashboards), written to the project folder.
  - Yours: every project created can be saved as a template (checked by default), and **Save as template** on a project page does the same. They appear first in Create project, marked as "yours", and × deletes them.
- **Single resources**
  - **Single container** (Containers tab of each engine), for one-off cases, in a tabbed form like Portainer's:
    - command, entrypoint, working directory, user and console (`-it`);
    - ports (IP, host port, container port and protocol) or `-P`;
    - volumes, host folders and tmpfs;
    - network, fixed IP, hostname, domain, MAC, DNS and extra `/etc/hosts` entries;
    - variables (as lines or as `.env` text) and labels;
    - restart policy, memory and CPU limits, `/dev/shm`;
    - privileged, init, read-only, capabilities, devices, sysctls and log driver;
    - engine (on hosts with more than one) and "always pull the image".
  - **Create volume** (driver, driver options, labels), **Create network** (driver, IPv4 and IPv6 subnets, gateway, IP range, internal, attachable, options and labels), **Pull image** and **Build image** (Dockerfile written in the app, a host folder as context respecting `.dockerignore`, or a git repository; with build args, target, no cache), in the sub-tabs of each engine.
  - Images: **Use** opens the container form with the image, **Name…** adds a tag, and under **⋯** there are **Layers** (the build history), **Push to registry…** and **Save .tar…**. Under **More**, **Load .tar…** (`docker load`) and **Registries (logins)…**.
  - **Private registries**: the login (ghcr.io, Docker Hub, a registry of yours) is kept in the keychain and Gantry uses it to pull and push images, also when bringing projects up. **Log in on <machine>** also writes the login to that machine's command line (`docker login --password-stdin`), for the compose and terminal there.
- **Projects**
  - Containers are grouped by Compose project, from the labels Compose writes itself.
  - Containers outside any compose appear under "Loose".
  - Project actions:
    - Up (`up -d`) and pull images (`pull`) use compose.
    - Stop, restart and remove go straight through the engine API, so they work even if the file was moved.
    - Every operation runs in the background, in a panel at the bottom with live output, the image download bar (bytes, percentage and layers) and **Cancel**. You can keep using the app; if the page reloads, operations in progress come back.
  - The `compose.yaml` (and the other project files, including `.env`) can be edited in the interface and is reread from disk every 3 s. If you edit it in Nvim, the screen follows; if you edit in both at the same time, the app warns about the conflict.
  - Shows which services and VMs differ from the file (the hash of `compose config` in containers; the definition each VM was started with, for VMs).
  - When the files change (through Gantry, your editor or a `git pull`), the project page says **The project files changed**, with what changes in each service and VM and the **Apply now** button. The project list marks "files changed". VMs running with the old definition restart only if you check it.
  - **Apply automatically** (File tab, off by default): applies when the files stop changing for a few seconds. A file with an error is not applied, and VMs never restart by themselves.
- **Containers**
  - Actions: start, stop, restart, pause, resume, kill and remove.
  - A page for each container (click the name): summary, ports, volumes, networks, variables, labels, limits and the inspect in JSON.
  - Connect and disconnect networks, with an optional fixed IP.
  - **Edit and recreate** and **Duplicate**, as in Portainer: the form opens with the current options. When editing, the current container is set aside until the new one is up; if something fails, it comes back.
  - Interactive shell and live logs, with a **line filter**, time range (15 min to 24 h, or all), timestamp per line and **Save…** to a file.
  - CPU and memory updated every 5 s.
  - **Files**: browse the container folders (running, with `sh`), **download** files (folders come as `.tar`), **upload** files and folders from this computer and delete. Download and upload work with the container stopped.
  - **Resources…**: changes CPU, memory, memory reservation, maximum processes and the restart policy on the fly, without recreating. Works on Docker, on Podman (restart policy only from 5.1) and on containerd (`nerdctl update`).
  - **Events**: what happened to the container (created, started, exited with a code, out of memory, resources changed…), in 1 h, 24 h or 7 days. The machine page has **Events** for everything (containers, images, volumes and networks).
  - **Create image…**: saves the current state of the container to a new image (`docker commit`).
- **Mixed labs and topology**
  - With the `x-gantry` extension, one compose splits its services between Docker and Podman (see below).
  - Topology tab in each lab: engines, networks, ports published on the host and dependencies that cross engines.
- **VMs in the lab**
  - With the `x-vms` extension, the lab also creates virtual machines in the QEMU of the host machine (see below).
  - **Manage…** on each VM: **snapshots** (with the VM running they also keep the memory; restoring resumes without rebooting), **grow the disk**, **clone** (the copy goes into the compose without the published ports) and **start with the machine** (LaunchAgent on a Mac, user systemd or crontab on Linux).
  - **Import disk…** in Create project converts OVA, VMDK, VDI, VHD(X) or raw to qcow2; for systems without cloud-init, `tela: true` turns on VNC.
  - The project backup includes the VM disks (optional), copied from a snapshot with the VM running.
- **Free ports**
  - In Create project, port variables (`${PORTA:-80}:80`) are checked against the machine: a port already in use (by a container of any engine or another program) becomes the next free one, with the reason under the field. On rootless Podman on Linux, ports below 1024 become 8000 + the port (80 → 8080).
  - If the project still does not come up because a port is taken, the output says who uses the port and which variable to change in `.env`.
  - The machine page shows its IP on the local network.
- **Web applications**
  - Published ports that answer HTTP or HTTPS (of containers and VMs) become links: the Open strip at the top of the project, the ports in the lists and the container page. Open uses the SSH tunnel when the host is a server.
  - Copy link gives the address through the machine's IP, to open on another device on the network. A container with the `gantry.path` label (e.g. `/grafana`) opens straight at that path.
- **Shared services**
  - A shared service is just a PostgreSQL, MySQL/MariaDB, Redis, RabbitMQ or MongoDB container running on the machine with its port published: Gantry finds it by itself, whether from a Gantry project or not (your `homelab-postgres`, for example).
  - To create one, **New shared service…** (in the Services tab of the machine or the project) chooses the kind and opens its template with the name filled in; then just generate.
  - The machine's **Services** tab lists them all, the port and which projects use each one. Each project's **Services** tab shows the ones it uses (address, database and user), its own and the others on the machine, with **Open psql** (or mysql, redis-cli, mongosh) logged in with the project's user and **Use in this project…**, which creates a database and user of its own and writes the connection to `.env` (PG_HOST, PG_PORTA, PG_USUARIO, PG_BANCO; password and URL in the keychain).
  - A template declares what it can use from outside with `x-gantry: usa:` (Paperclip's already does). When generating the project, the wizard offers an existing service and, by default, creates a **database and user only for the project**, with a new password (a vhost on RabbitMQ). The template variables are filled in by themselves.
    ```yaml
    x-gantry:
      usa:
        postgres: {host: PG_HOST, porta: PG_PORTA, usuario: PG_USUARIO, senha: PG_SENHA, banco: PG_BANCO}
    ```
  - The project reaches the service through `host.containers.internal` and the published port; the template compose maps that name with `extra_hosts: ["host.containers.internal:host-gateway"]`, which works on Docker and Podman.
- **Backup**
  - **Backup** tab in each project: copies the volumes (through the engine API, so it works for servers too), a dump of each database the project runs (`pg_dumpall`, `mysqldump`/`mariadb-dump`, `mongodump`) and the project folder, into a `.tar.gz` **in the folder you choose** on this computer (the app asks on the first backup and remembers per project; **Change…** changes it). **Restore from a file…** accepts a backup from any folder.
  - Options: stop the containers while copying the volumes (consistent copy) and include `.env`.
  - **Restore** removes the containers, recreates the volumes with the backup content and brings them up again (the project files come back if you check it). The dumps stay in the archive to restore by hand into another database.
  - **Automatic backup**: every 6 h, 12 h, day or week, keeping the last N. Runs while Gantry is open.
- **Secrets in the keychain**
  - Passwords and tokens in `.env` (variables with SENHA, PASS, SECRET, TOKEN, KEY…) can be kept in the **macOS Keychain** (or Secret Service on Linux; without a keychain, in a file only you can read in the config folder). `.env` keeps only a comment.
  - When generating a project, this comes checked. In an existing project, the File tab has **Move to the keychain** and **Back to .env**.
  - The app hands the values to compose at run time: on this computer through the process environment and on servers through the shell's standard input, so they do not appear in `.env` or in the server's process list. Sending to another machine takes the secrets along when you check `.env`.
- **Image versions**
  - Gantry's templates use fixed versions (no `latest`); the few that follow a moving tag take it from a variable.
  - Projects with `build:` services get the **Rebuild** button: builds the images again and recreates the containers (`compose up -d --build --force-recreate`). Use it after changing the code, since the compose itself did not change.
  - The project's Containers tab shows, for each versioned image, the newest of the same line (e.g. 11.1.0 → 11.6.3) and of another major version (→ 12.0.2), asking the registry (Docker Hub, ghcr.io, quay.io…). **Use** writes the version to the compose or `.env` (when a variable sets the tag); then Pull images and Up.
  - For tags without a version (`latest`, `main`…), compares the machine's image with the registry's and says "new image in :latest".
  - The project list marks the ones with updates. Projects from a git repository show, in the File tab, how many new commits the remote has.
  - Nothing is updated by itself: the app only tells you.
- **Gantry versions**
  - The app checks the release pages (this repository's releases and their copy in homebrew-tap, every 6 h) and shows **↑ version** at the bottom of the sidebar, with the notes; it does not download or install anything. Clicking the version at the bottom checks right away.
- **Export, import and send**
  - Export creates a `.tar.gz` with the project folder (`.env` only if you check it), where you choose. Import package, under Other ways, opens that package in a new folder and brings it up.
  - Send to… copies the project to another machine (remote deploy) and brings it up there. In Create project, Target machine does the same for a new project, with the ports checked on the chosen machine.
  - Volumes and VM disks do not go along: the project starts from scratch at the destination.
- **Alerts**
  - If there is a Prometheus with rules (the Observability lab comes with some), firing alerts appear at the top of the machine and the project. Silence hides them per machine; Settings shows them again.
- **Project files**
  - The File tab edits, besides the compose and `.env`, the other text files in the folder (prometheus.yml, Traefik routes, dashboards…). Save and restart restarts only the services that mount that file.
- **Metrics**
  - A Metrics tab in each project, with CPU and memory charts per container and per VM (15 min to 24 h) and a summary table with the VM disks. Click a series in the legend to highlight it.
  - Container page: current CPU and memory and the history.
  - Container and VM lists with live CPU, memory and (for VMs) disk.
  - VMs: the app reads the QEMU process on the machine (CPU, the memory it takes and disk space), with nothing installed inside the VM. On a Mac, memory is the same as Activity Monitor's (counts the compressed part). For metrics from inside the guest, use node_exporter in the VM (as in the Observability lab).
  - Without the agent, the app collects while it is open (up to 6 h). With the agent, the history of containers and VMs stays on the server, even with the app closed.
- **Optional agent** (see below)
- **Observability** (Prometheus, Loki and Grafana; see below)
- **Images, volumes and networks**
  - Tabs on the host page with each item, what uses each one and the option to remove.
  - "Clean unused" removes what no container uses.
- **Where to save**: everything that writes or opens files on this computer asks where, with the system picker (Finder on a Mac, Windows', zenity or kdialog on Linux): backups, export, import and the folder of new projects on this machine (**Choose…** next to the field). On a server over SSH (or a Linux without zenity), Gantry opens its own folder browser, which lists the server's folders and creates new ones.
- **Welcome**: the first time (with no servers or clusters of yours), a screen shows what Gantry found on this computer, offers the servers in `~/.ssh/config` and the clusters in `~/.kube/config` with one click each, and a test Postgres + Adminer with the password already created. It comes back through Settings → General → Open the welcome screen.
- **Tools**: a tab on each machine (this one and the servers): what Gantry uses there (engine, compose, QEMU, ISO generator, curl, KVM acceleration, the project network bridge, helm, git, ssh, python3, dynamips), what each is for and the install command for that machine's system (Homebrew, apt, dnf, pacman, apk, zypper). On a Mac, what comes from Homebrew installs with a button. The project page and Create project warn when something the project uses is missing (QEMU for VMs, compose), and a cluster that signs in through a plugin that is not installed (aws, gke-gcloud-auth-plugin, kubelogin…) shows how to install it.
- **AI assistants (MCP)**: gear › General › **Assistants (MCP)**. An AI assistant (an MCP client) queries labs, containers, VMs and clusters through this Gantry while it is open: hosts, projects and their drift, container logs, cluster resources, pod logs, events, Helm releases. With write permission it can also bring projects up and down, restart containers and scale or restart workloads. Secrets and Helm values never leave; read-only clusters stay read-only. The client runs `gantry mcp` (the card shows the exact command and file).
- **Splash**: a short presentation with the name and "by GonService", for at least 1 s or while the machine list loads.

## Running on macOS

Requires Go 1.22+, [Wails](https://wails.io) v2 and the Xcode command line tools.

```bash
make deps     # installs wails
make run      # builds and opens the app
```

Other targets:

| Target | What it does |
|---|---|
| `make` / `make desktop` | Builds `build/bin/Gantry.app` (on Linux, `build/bin/gantry`) |
| `make dev` | Wails development mode |
| `make web` / `make run-web` | Version without a window, which opens in the browser |
| `make linux` | The Linux desktop app and its package |
| `make release` | Distribution builds |
| `make agents` | Only the agent binaries (the other targets already run this) |
| `make test` | Tests |
| `make clean` | Deletes the builds |

The app is not signed or notarized (that needs an Apple Developer account). The first time, open it with right-click > Open, or run `xattr -dr com.apple.quarantine build/bin/Gantry.app`.

## Running on Linux

The Linux desktop app uses WebKitGTK 4.1 for the window.

**Install with one command:** `curl -fsSL https://gonservice.github.io/gantry/install.sh | sh` downloads the newest version, checks it against `SHA256SUMS` and installs it as below (on a Linux without a desktop, or with `sh -s -- --server`, the server version in `~/.local/bin`). The same script works on macOS (Homebrew when there is one); on Windows, `irm https://gonservice.github.io/gantry/install.ps1 | iex`. `GANTRY_VERSION=X.Y.Z` picks a version.

**Install a published version by hand:** download `Gantry-VERSION-linux-amd64.tar.gz` from the release, unpack it and run `./instalar.sh`. It installs for your user, without sudo: the program in `~/.local/bin/gantry`, the application menu entry and the icon. `./instalar.sh remover` undoes it; the hosts, labs and clusters in `~/.config/gantry` stay. If WebKitGTK is missing, the script tells you the command:

- Fedora: `sudo dnf install webkit2gtk4.1`
- Debian/Ubuntu: `sudo apt install libwebkit2gtk-4.1-0`
- Arch: `sudo pacman -S webkit2gtk-4.1`

**Build:** needs Go, Node and the WebKitGTK development packages (Fedora: `sudo dnf install webkit2gtk4.1-devel gtk3-devel`; Debian/Ubuntu: `sudo apt install libwebkit2gtk-4.1-dev libgtk-3-dev`).

```bash
make linux    # build/bin/gantry and the package dist/Gantry-VERSION-linux-ARCH.tar.gz
make run      # builds and opens
```

On Linux, "this machine" is the computer itself: Gantry finds its Podman and Docker, and VMs and network labs run right there.

### Browser mode

The same binary runs without a window with `-web` (or built without the `desktop` tag). It prints a link that works once (for 10 minutes) and opens the app in that browser; once used, it prints another, for another browser.

| Flag | Default | Description |
|---|---|---|
| `-web` | `false` | Opens in the browser instead of the window. |
| `-addr` | `127.0.0.1:7777` | Listen address. If the port is taken, uses another free one. |
| `-config` | see below | Configuration file. |
| `-no-browser` | `false` | Does not open the browser. |
| `-expor-na-rede` | `false` | Allows an `-addr` outside localhost (prefer an SSH tunnel; if you use it, turn on `-tls`). Without HTTPS, every page shows a warning. |
| `-tls` | `false` | HTTPS with a certificate Gantry makes and keeps in the config folder (`tls/`), for this computer's names and addresses. The browser asks to trust it the first time: compare the SHA-256 fingerprint printed at the start. |
| `-cert`, `-key` | | HTTPS with your own certificate and key (PEM files). |
| `-version` | | Shows the version. |

## Remote hosts

In the interface, use **Add host** with something like `ssh://homelab` or `ssh://user@server:22`.

How the connection works:

1. The app opens a master SSH connection (ControlMaster). Passwords and key passphrases are asked in the interface.
2. A short script looks for the engine socket on the server.
3. The app forwards that socket to a local socket with `ssh -O forward`.
4. The same connection runs compose and reads and writes the lab files.

The remote user needs access to the socket. On Docker, that means being in the `docker` group. On rootless Podman, the user's `podman.socket` must be active; the app offers a button to activate it.

## containerd and k3s

These runtimes have no Docker API, only a command line. Gantry runs a translator (shim) that serves the part of the Docker API the app uses and calls `nerdctl` underneath. So the same screens work with them: labs, containers, logs, shell, CPU and memory, images, volumes and networks.

| Address | Runtime |
|---|---|
| `nerdctl://default` | containerd through `nerdctl`, in the `default` namespace |
| `nerdctl://ns?address=/run/x.sock&sudo=1` | another namespace, another containerd and, if needed, `sudo -n` |
| `k3s://` | the containerd of k3s (namespace `k8s.io`); pods appear as `k8s-<namespace>` labs |

On Linux, the app finds `nerdctl` and k3s by itself, using `sudo -n` when it works without asking for a password.

On servers, the agent does the translation: when it starts, it publishes `nerdctl.sock` and `k3s.sock` next to its own socket. On the host page, the **Other engines on this server** list shows these engines (and Podman, if there is also a Docker), with the **Add as host** button.

Limits:

- Labs with compose use `nerdctl compose`.
- The shell works through a terminal created with the system's `script`, without resizing.
- Stopping k3s containers does not help, because the kubelet recreates them.

## Kubernetes clusters

Gantry talks straight to the Kubernetes API (no `kubectl`), with the kubeconfig credentials: certificates, token, user and password, or a plugin such as `gke-gcloud-auth-plugin` and EKS's `aws`.

Every connection, including pod shells, goes through the kubeconfig proxy (`proxy-url`) or the system's `HTTPS_PROXY`. The login plugin runs once even with several screens asking at the same time, and the list of cluster kinds is read again every 10 minutes (or right away, when a new kind, like a freshly installed CRD, does not appear yet).

To add a cluster: gear → **Clusters** → **Add cluster**. You can choose a context from `~/.kube/config` (the app rereads the file when it changes) or paste a kubeconfig, kept in a copy of the app's own. A pasted kubeconfig that runs a program, reads files of this computer, sends the connections through a proxy (`proxy-url`) or does not check the cluster's certificate (`insecure-skip-tls-verify`) only goes in after you confirm, with each of these listed.

Each cluster has a mode, chosen in Settings:

| Mode | What it allows |
|---|---|
| **Normal** (default) | everything your account can do in the cluster, without asking which cluster it is; only actions like delete or restart ask to confirm the action itself |
| **Read-only** | see everything except Secret values; no shell, no port forwarding, no changes |

In read-only mode you also cannot forward ports, and switching a cluster to read-only closes the open ones. Helm release values and manifests (which often hold passwords) do not appear.

**Open** on a Service creates a local tunnel (127.0.0.1, random port) through the API proxy, without `port-forward`. The tunnel only accepts whoever opened it from the app and closes when the cluster is removed or changes.

### Projects that create a cluster

A project with VMs declares where the API is:

```yaml
x-gantry:
  kubernetes: {vm: k3s-server, porta: 6443}

x-vms:
  k3s-server:
    ports: ["6443:6443"]
    ...
  k3s-agente-1:
    depois: k3s-server     # only starts when the server answers over SSH
    ...
```

When the project comes up, the VMs start in the order of `depois` ("after"; a cycle is an error). When the API answers, Gantry reads the VM's `k3s.yaml` (or `admin.conf`), points it to the published port and puts the cluster in the sidebar, in the machine's folder, in Normal mode. The first time, k3s takes a few minutes to install. Removing the project's VMs removes the cluster too.

The **k3s cluster (3 VMs)** template already comes this way.

## Mixed labs (`x-gantry`)

A lab is still a regular compose. The `x-gantry` extension, ignored by compose tools, says which engine each service runs on:

```yaml
x-gantry:
  runtime: podman        # lab default (without it, the host engine)

services:
  prometheus:
    image: prom/prometheus
    ports: ["9090:9090"]
  grafana:
    image: grafana/grafana
    ports: ["3000:3000"]
    depends_on: [prometheus]
    x-gantry:
      runtime: docker    # only this service goes to Docker
```

When bringing the lab up, Gantry:

- splits the services by engine;
- brings up first the engine the others depend on;
- runs compose once per engine, with `DOCKER_HOST` pointing to the right socket and `--no-deps`.

Stop, restart and remove go through all engines. The lab appears whole, with each container's engine, on the page of either host.

Requirements and limits:

- Both engines must be on the same machine. On a Mac, they are two local engines (OrbStack or Docker Desktop, plus `podman machine`). On an SSH server, the app finds the Docker and Podman sockets by itself.
- Ideally `docker compose` is installed, because it talks to both engines. Without it, the app uses `podman compose` for the Podman services.
- Different engines do not share networks. A service only reaches one on another engine through the port published on the host (`host.docker.internal` on a Mac, or `extra_hosts: ["host.docker.internal:host-gateway"]`). The Topology tab warns when a port is missing.
- The Metrics tab shows only the containers of the host's main engine.

A complete example is in `examples/observabilidade`: Prometheus and Loki on Podman, Grafana on Docker, with the datasources already configured.

## VMs in the lab (`x-vms`)

The same compose can declare virtual machines. They run in the QEMU of the host machine: on a Mac with HVF, on a Linux server with KVM (or emulated, slower, if KVM is not available).

```yaml
x-vms:
  web:
    image: debian-12          # debian-13, ubuntu-24.04, ubuntu-22.04, a URL or a path to a .qcow2
    cpus: 1
    memory: 1g
    disk: 8G
    ports: ["8081:80"]        # host port → VM port
    packages: [nginx]         # installed by cloud-init on first boot
    run:                      # commands on first boot
      - echo ok > /var/www/html/ok.txt
    cloud-init: |             # optional: more cloud-config, merged with the rest
      timezone: America/Sao_Paulo
```

When the lab comes up, VMs start before the containers. For each VM, the app:

- downloads the image once to `~/.cache/gantry/images`;
- creates a disk of its own that only keeps the differences from the image;
- generates the cloud-init with the `gantry` user and an SSH key just for VMs;
- starts QEMU in the background.

Everything about a VM is in `~/.local/state/gantry/vms/<lab>/<vm>/`. In the Containers tab, the Virtual machines section has:

- Shell: a terminal in the VM inside Gantry (SSH with the VM key; on a server, through the SSH tunnel);
- Logs: the VM system logs live (`journalctl -f`, or syslog where there is no journald);
- Console: the serial output, which shows the boot and cloud-init before SSH is ready;
- clicking the SSH port copies the `ssh` command to use in a terminal of yours;
- Stop, Start, Restart and Remove. Remove deletes the disk.

Containers have Shell and Logs (live stdout and stderr) in the lists and on each one's page.

With `depois: <another VM>`, the VM only starts when the other already answers over SSH (useful for cluster nodes to wait for the server).

Stop shuts the VM down through ACPI; if it does not answer, QEMU is ended. Restart applies CPU, memory and port changes. Packages, `run` and `cloud-init` only apply to a new VM, so remove the VM and bring the lab up again.

Each VM's network is QEMU's NAT: the VM reaches the host at `10.0.2.2`, and containers reach the VM through the published ports. The SSH port is chosen from the lab and VM names, or fixed with `ssh: 2230`.

Requirements on the host machine:

- On a Mac: `brew install qemu`.
- On Fedora: `sudo dnf install qemu-kvm qemu-img genisoimage`.
- On Debian or Ubuntu: `sudo apt install qemu-system qemu-utils genisoimage`.

Example: `examples/vm-nginx` brings up a Debian VM with Nginx at `http://localhost:8081`.

### Network equipment (IOL, Dynamips, VPCS)

Cisco routers and switches (IOL and Dynamips) and VPCS computers also go in `x-vms`, wired by cables. Cards, NPE, memory, startup config, images, what to install on the machine and importing PNETLab and EVE-NG labs are in [network-labs.md](network-labs.md).

### Project network (`lan: true`)

With `lan: true` on a VM (or `x-gantry: {lan: true}` for all), it gets a second NIC on a machine network, with a fixed IP (from `.100` to `.250`). The first NIC stays the same: internet, SSH and published ports.

- The project VMs find each other by name (Gantry writes their `/etc/hosts` at every boot).
- The project containers find the VMs by name (`http://web/`): when bringing up, Gantry generates `.gantry/rede.yaml` with `extra_hosts` and passes it to compose along with your file, which is not changed.
- VMs reach the containers through the published ports, at the machine's IP on that network (service names and `gantry-host` are also in `/etc/hosts`).
- The Network column in the VM lists shows each one's IP.

The network is created once per machine, as administrator. The machine's VMs tab shows whether it exists and the command to create it:

- **Mac:** `brew install socket_vmnet && sudo brew services start socket_vmnet` (the same used by Lima). Network `192.168.105.0/24`.
- **Linux and WSL2:** a `gantry0` bridge (`10.213.0.0/24`) with `qemu-bridge-helper` allowed on it. The suggested command also creates a systemd service so the bridge comes back after a reboot.
- **Windows:** through WSL2 (see below).

**Bridge mode.** The VMs tab of the machine › **Network mode…** chooses between the shared network above and **bridge**: the VMs go on the network the machine itself is on (your home's or the office's) and any device on it reaches them by IP. Use it when the shared network does not work, for example with a VPN such as Cloudflare WARP. Gantry gives each VM a fixed address from a range you choose (by default `.200` to `.230`), outside the router's DHCP, and skips addresses that answer on the network. The VMs already created change network only after being removed and created again.

- **Mac:** a second socket_vmnet, bridged to the interface (`en0`), as a launchd service; the VMs tab shows the command (it needs administrator once). It works over Wi-Fi too.
- **Linux:** a bridge of the system with the network card (for example `br0`, made in NetworkManager or netplan) and `allow br0` in `/etc/qemu/bridge.conf`; the VMs tab shows what is missing.

Templates that use the network: **k3s cluster (3 VMs)** and **Network: VMs and containers**. An existing VM only joins the network after being removed and recreated.

## Observability

If there is a Prometheus, a Loki or a Grafana near the host, Gantry uses them:

- **Prometheus:** the labs' Metrics tab gets the Prometheus source, with cAdvisor CPU and memory per container and a field for any PromQL query.
- **Loki:** each container's log window gets a selector (Live, or Loki with 1 h, 6 h or 24 h) to see the history.
- **Grafana:** the host page lists the dashboards and opens any of them in the browser.

The app finds these services among the machine's containers, by image and published port. You can also give the addresses in **Observability › Configure**, on the host page, including a Grafana service account token.

The addresses are seen from the host machine. On an SSH server, `http://127.0.0.1:9090` is the server's own Prometheus: the app reaches it through the same SSH connection and, to open Grafana in the browser, forwards a local port.

For logs, Loki needs the `container` labels (or `container_name`, or `compose_project` with `compose_service`).

The `examples/monitoramento` example brings everything up on a machine's Docker:

- Prometheus and cAdvisor;
- Loki and Alloy, which collects the container logs;
- Grafana, with the datasources and one dashboard per lab already configured.

## Agent (optional)

SSH hosts work with nothing installed on the server. The agent is a small service (`gantry-agent`, about 6 MB) that adds:

- 24 h of CPU and memory history for containers **and VMs**, collected every 15 s even with the app closed and saved to disk;
- immediate notice (inotify) when a lab's compose changes on disk, instead of reading the file every 3 s;
- the **direct connection**: TCP with mutual TLS, without going through SSH (see below).

To install, use **Install agent** on the host page. The app:

1. Finds the server architecture (Linux amd64 or arm64) and sends the binary embedded in it to `~/.local/bin`.
2. If the direct connection is checked (default), pairs the agent with this app (below).
3. Creates the `~/.config/systemd/user/gantry-agent.service` service, enables it and runs `loginctl enable-linger`, so the agent keeps running without an open session. Without user systemd, it starts with `nohup`.
4. Forwards the agent socket (`$XDG_RUNTIME_DIR/gantry/agent.sock`) over the SSH connection, kept as a fallback.

The agent has its own protocol version: the app only asks to update it when it needs something the installed agent does not have, not at every new app version. **Remove** deletes the service, the binary, the history and the pairing; the project VMs (in the same state folder) are not touched.

### Direct connection (TCP + mutual TLS)

- Each side has its own certificate (ECDSA P-256, generated on its machine; private keys never leave it). Pairing runs over SSH: the app sends its certificate and receives the agent's. From then on, each side only accepts the other's (pinned) certificate, with TLS 1.3.
- The agent listens on port 7781. The app uses the direct connection for the agent API and for the engine's (containers, logs, shell, statistics). Commands (compose, files, VMs) still go through SSH.
- If the port does not answer (firewall), the app falls back to SSH by itself and tries again after a minute. The host page shows which path it is using and has **Turn direct connection on/off**.

## Windows (WSL2)

The Windows app (`Gantry-X.Y.Z-windows.zip` in the releases) is experimental: it has not been tested on a real Windows yet.

- On Windows, each WSL2 distribution appears as a machine (`wsl://Ubuntu`), without SSH: commands run through `wsl.exe` and the app keeps the distribution on while it is open.
- On first use the app installs and pairs the agent inside the distribution, listening only on `127.0.0.1:7781` (WSL2 forwards it to Windows); through it the app talks to the Docker or Podman inside WSL.
- VMs (`x-vms`) use the distribution's QEMU, with KVM when Windows 11 has nested virtualization; the project network is the `gantry0` bridge, as on Linux.
- Docker Desktop (`docker-desktop*` distributions) is ignored; install Docker or Podman in your distribution. Servers over SSH from Windows are not supported yet (Windows OpenSSH has no ControlMaster).

## Tests

```bash
make test        # unit and API tests (fake engine and CLIs), no engine needed
make e2e         # full life cycle against the real engines of this machine
```

`make e2e` tests every engine the app finds (Docker, Podman, containerd), going through:

- bringing a lab up;
- logs, shell and statistics;
- topology;
- stop and start;
- remove.

It also tests a mixed lab, when there are Docker and Podman. Optional variables:

- `GANTRY_E2E_SSH=ssh://server` includes a server;
- `GANTRY_E2E_VM_IMAGE=debian-12` includes a VM;
- `GANTRY_E2E_IMAGE` changes the test image;
- `GANTRY_E2E_ONLY` limits the hosts tested.

`make e2e-real` runs the tests with the real things instead of the fakes of the unit tests: a VM in QEMU (`GANTRY_E2E_VM_IMAGE=debian-12`: the web server it installs answers on the published port, the samples of the QEMU process, the node_exporter inside it, a port added and removed with it running) and the network devices with real images (`GANTRY_E2E_C7200` and `GANTRY_E2E_IOL`, paths of the images: the startup config goes in, a wrong line on purpose shows as refused, a VPCS pings the router and, on Dynamips, the idle-PC is calculated). On GitHub, the **Testes reais** workflow runs the VM with KVM every Monday, before each version and on demand; the network devices run only on your machine, because Cisco images cannot go to GitHub.

The interface has a quick test that opens every screen in both themes and fails on JavaScript errors: `tests/ui_smoke.py`, which uses Playwright. The manual test script for the Mac is in [testes.md](testes.md) (in Portuguese).

## Releases and Homebrew

The repository is `GonService/gantry`. Each `vX.Y.Z` tag runs GitHub Actions (`.github/workflows/release.yml`), which tests, builds and publishes the release with:

- `Gantry-X.Y.Z-macos.zip`: the Mac app (Apple Silicon and Intel in the same file, unsigned);
- `Gantry-X.Y.Z-windows.zip`: the Windows app (experimental, unsigned);
- `Gantry-X.Y.Z-linux-amd64.tar.gz`: the Linux desktop app (x86_64), with `instalar.sh`;
- `gantry-servidor-X.Y.Z-linux-<amd64|arm64>.tar.gz`: the server version (no window; the interface opens in the browser), for a Linux without a screen;
- `SHA256SUMS`.

The release is published here, with the `vX.Y.Z` tag. [GonService/homebrew-tap](https://github.com/GonService/homebrew-tap) picks it up by itself (its Gantry workflow, every hour or when run by hand, with only its own token): it copies the release there with the `gantry-vX.Y.Z` tag (apps up to 0.47 look for new versions only there; versions up to 0.47.1 were published only there) and updates the cask (`Casks/gantry.rb`, generated by `scripts/cask.sh`, downloading from this repository) and the download links of the pages there (`scripts/downloads.py`). No secret is needed. The pipeline does not use Actions artifacts (the organization's storage quota blocks builds when full): the release is born as a draft, each build sends its files straight to it and the last job publishes it. On a Mac:

```bash
brew install --cask gonservice/tap/gantry
brew upgrade --cask gantry
```

The cask removes the "downloaded from the internet" mark only from Gantry.app, so macOS does not block the first launch. `brew uninstall --zap --cask gantry` also deletes the configuration (machines, clusters, preferences); containers and VMs stay on the machines.

To release a version: change `VERSION` in the Makefile, commit and run `git tag -a vX.Y.Z -m "Gantry X.Y.Z" && git push origin main vX.Y.Z`. The app footer tells you when there is a newer release: it reads this repository's releases and the homebrew-tap ones (starting with `gantry-v`) and takes the newest. Installed through Homebrew, the notice shows the `brew upgrade` to copy.

Each push to `main` runs the tests and the interface test, which opens every screen of the browser version with GitHub's Docker and fails on JavaScript errors (`.github/workflows/ci.yml`). Changes to `site/` or `docs/images/` publish the website (`.github/workflows/pages.yml`).

## Configuration

It lives in `~/Library/Application Support/gantry/config.json` on macOS and `~/.config/gantry/config.json` on Linux. It is created when you add a host, but can also be written by hand:

```json
{
  "autodetect": true,
  "hosts": [
    { "name": "homelab", "url": "ssh://homelab" },
    {
      "name": "laboratorio",
      "url": "ssh://ana@192.0.2.10",
      "socket": "/run/user/1000/podman/podman.sock",
      "compose": ["podman", "compose"]
    }
  ]
}
```

- `url`: `ssh://…`, `unix:///path/to.sock`, `tcp://host:2375`, `nerdctl://…` or `k3s://`. `tcp://` hosts only control containers, without compose or files.
- `socket`: optional; without it the app looks for the socket by itself.
- `compose`: optional; the default is `podman compose` on Podman and `docker compose` on the others.
- `autodetect: false`: turns off detection of local engines.
- `hidden`: detected hosts you hid.

The labs the app has seen are in `state.json`, in the same folder.

### Log, copies and diagnostics

- **Log:** `logs/gantry.log` in the configuration folder. Past 5 MB, the file becomes `gantry.log.1` (up to `.3`). It records: startup, operations (start, end, duration and error), SSH failures and internal errors. Passwords, tokens and keys are masked.
- **Internal errors:** a programming error in a background task (cluster watch, metrics, operations) is logged and the app stays open. An operation that breaks ends with "Gantry internal error…" in its panel.
- **Format and copies:** `formato.json` says which format version the folder is in. When opening a folder of an older format, Gantry makes a copy in `copias/antes-do-formato-N-…` and only then migrates. A folder from a newer Gantry is not changed. Also, the app keeps one copy per day of the settings (`copias/diaria-YYYYMMDD`, the last 7), with the `.json` files and the kubeconfigs, without the log. To bring a configuration back, close the app and copy the files from the copy to the folder.
- **Diagnostics:** Settings → General → **View diagnostics** gathers the version, the system, the state of each machine and cluster, the engines of this computer, internal errors with the stack and the end of the log, without addresses, users, passwords or your home folder. **Copy** or **Save…** to send with the description of the problem; **Open log folder** shows the file.

## Test VM

`scripts/test-vm.sh` brings up a Fedora VM with Podman in the Mac's QEMU, to test Gantry against a real remote host:

```bash
brew install qemu
scripts/test-vm.sh start    # downloads the image the first time and starts the VM
scripts/test-vm.sh ssh      # enters the VM
scripts/test-vm.sh status
scripts/test-vm.sh console  # follows the VM console
scripts/test-vm.sh stop
scripts/test-vm.sh reset    # deletes the disk and starts from scratch
```

The VM creates a user with your name, accepts your SSH keys and has the password `gantry`. It comes with Podman, `podman.socket` on and an example compose in `~/labs/web`. At the end of `start`, the script shows the address to add in Gantry (`ssh://<you>@127.0.0.1:2222`).

To test the button that activates the Podman socket, start with `GANTRY_VM_SOCKET=off`. The other options are at the top of the script.

## Security

- By default, the internal server only listens on `127.0.0.1`. Listening on the network needs `-expor-na-rede`; an SSH tunnel is recommended. With `-tls` (a certificate Gantry makes) or `-cert` and `-key` (yours), the connection uses HTTPS; without them, every page warns that anyone on the network can read the key. The programs of the computer itself (SSH password prompts, `gantry mcp`) always talk to the server over `127.0.0.1`.
- Every API call carries a token generated at each run, in the `Authorization` header (or in the URL, for live streams). There is no cookie: a `127.0.0.1` cookie would also go to every other app on `127.0.0.1`, including container pages and forwarded ports.
- In browser mode, the terminal link (`?c=`) works once and for 10 minutes; it hands the token to the page, which keeps it in that origin's local storage. Once used, the terminal shows another, for another browser. The token never goes on another program's command line.
- Requests from other origins and from hosts other than localhost are refused, which protects against CSRF and DNS rebinding.
- **Open** on a cluster app creates a local tunnel with a one-use link and a cookie of that port only; with the cluster in read-only mode, only reads go through the tunnel.
- The kubeconfig read from inside a project VM (k3s) cannot run commands or read files of this computer: login plugins, files and proxy are dropped.
- The SSH tunnel folder in `/tmp` must belong to the user and be closed to others; if not, the app uses a new one.
- The log masks passwords, tokens, keys and passwords in URLs before writing.
- Restored packages and backups do not write outside the project folder, not even through symbolic links already there.
- Imported labs (`.unl`) and packages cannot inject compose keys or shell commands: names, paths and image references are checked before anything is written.
- Typed passwords stay only in memory, and only if you check "remember", until the app closes.
- Project passwords and tokens can be kept in the system keychain instead of `.env` (see Secrets).
- The direct connection to agents uses TLS 1.3 with pinned certificates on both sides, paired over SSH. An unpaired app does not connect, and the app does not accept an agent other than the one it paired with.

## Structure

```
main.go, desktop.go      flags, internal server and window (Wails, desktop tag)
cmd/gantry-agent         optional agent that runs on servers
askpass.go               SSH_ASKPASS helper that forwards ssh questions to the interface
envpath.go               full PATH for apps opened from Finder
importcmd.go, mcpcmd.go  gantry importar-unl and gantry mcp
internal/agent           agent server and client (Unix socket, metrics, inotify)
internal/agentbin        agent binaries embedded in the app (generated by make agents)
internal/composex        reading the compose as written and the x-gantry and x-vms extensions
internal/config          configuration and detection of local engines
internal/engine          Docker/Podman API client (HTTP over the socket)
internal/hosts           local, tcp and ssh connections; remote commands and files
internal/labs            grouping by compose project and remembered projects
internal/sysinfo         machine hardware and usage (sh script for Linux and macOS)
internal/templates       project templates: the built-in ones (builtin/*.yaml) and the user's
internal/local           state and installation of this machine's engines
internal/metrics         CPU and memory collection and history per container
internal/vms             QEMU VMs and network devices of the labs (embedded vmctl script, runs on the host machine)
internal/unl             PNETLab/EVE-NG import
internal/mcp             MCP server for AI assistants
internal/obs             Prometheus, Loki and Grafana clients, and finding them among the containers
internal/shim            Docker API for command line runtimes (nerdctl, k3s)
internal/kube            Kubernetes API client (kubeconfig, lists, apply, logs, exec over WebSocket, Helm)
internal/server          REST API and SSE streams (events, logs, shell, passwords)
frontend/                React + TypeScript interface (Vite); make ui builds it into web/ui/
                         (src/shell: sidebar, routes and bottom panel; src/views: the screens)
web/                     what is embedded in the binary: index.html, CSS, xterm.js and web/ui/
site/                    the website (GitHub Pages)
scripts/test-vm.sh       Fedora test VM in QEMU
examples/                example labs (examples/README.md)
e2e/, tests/             end-to-end and interface tests
```

## License

Copyright (C) 2026 GonService.

Gantry is free software under the [GNU Affero General Public License v3.0](../LICENSE) (AGPL-3.0): you can use, study, change and share it, as long as modified versions, including those offered to other people over a network (Gantry's browser mode), are also shared with their source code, under the same license.

Bundled third-party components keep their own licenses: VPCS (BSD, `internal/vpcsbin/bin/COPYING`) and xterm.js (MIT, `web/vendor/xterm-LICENSE`). Cisco device images (IOL, Dynamips IOS) are not part of the project and are not distributed here.

Found a security issue? Do not open a public issue: see [SECURITY.md](../SECURITY.md).
