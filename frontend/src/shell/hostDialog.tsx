// Add or edit a host (a server over SSH, a socket, tcp://…).
import { useState } from "react";
import { api, enc } from "@/lib/api";
import { hostByName, loadHosts, useStore, type Host } from "@/lib/store";
import { hostHref } from "@/lib/app";
import { Modal, openDialog } from "@/components/dialog";
import { FolderSelect, moveToFolder } from "@/views/settings/folders";
import { toast } from "./toasts";
import { t } from "@/lib/i18n";

export function openHostDialog(hst?: Host) {
  openDialog((close) => <HostForm hst={hst && hst.name ? hst : undefined} close={close} />);
}

function HostForm({ hst, close }: { hst?: Host; close: () => void }) {
  const groups = useStore((s) => s.groups);
  const [name, setName] = useState(hst?.name ?? "");
  const [url, setUrl] = useState(hst?.url ?? "");
  const [socket, setSocket] = useState(hst?.socket ?? "");
  const [compose, setCompose] = useState((hst?.composeConfig || []).join(" "));
  const [folder, setFolder] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Modal
      title={hst ? t("Editar host") : t("Adicionar host")}
      onClose={close}
      onSubmit={async (e) => {
        e.preventDefault();
        const body = { name: name.trim(), url: url.trim(), socket: socket.trim() || undefined, compose: compose ? compose.split(" ") : undefined };
        if (body.url && !/^(ssh|unix|tcp|nerdctl|k3s|wsl):\/\//.test(body.url)) body.url = "ssh://" + body.url;
        setBusy(true);
        try {
          if (hst) {
            await api("/hosts/" + enc(hst.name), { method: "PUT", body });
            toast(t("Host salvo"), "ok");
          } else {
            await api("/hosts", { method: "POST", body });
          }
          close();
          location.hash = hostHref(body.name);
          await loadHosts(true);
          if (!hst && folder) {
            const nh = hostByName(body.name);
            if (nh) await moveToFolder(nh.machine || "host:" + nh.name, folder);
          }
        } catch (x) {
          setErr((x as Error).message);
        }
        setBusy(false);
      }}
      actions={
        <>
          <button type="button" className="btn btn-ghost" onClick={close}>
            {t("Cancelar")}
          </button>
          <button type="submit" className="btn" disabled={busy}>
            {hst ? t("Salvar") : t("Adicionar")}
          </button>
        </>
      }
    >
      <div className="field">
        <label htmlFor="hf-name">{t("Nome")}</label>
        <input id="hf-name" required placeholder="homelab" autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
      </div>
      <div className="field">
        <label htmlFor="hf-url">{t("Endereço")}</label>
        <input id="hf-url" required placeholder={t("ssh://homelab ou ssh://usuario@servidor")} autoComplete="off" spellCheck={false} value={url} onChange={(e) => setUrl(e.target.value)} />
        <p className="hint">
          {t("Para servidores, use")} <code>ssh://</code> {t("com qualquer alias do seu")} <code>~/.ssh/config</code>. {t("Se pedir senha (do servidor ou da chave), o Gantry pergunta na hora. Para um engine nesta máquina,")}{" "}
          <code>{t("unix:///caminho/do.sock")}</code>. {t("Também aceita")} <code>tcp://host:2375</code>, <code>nerdctl://namespace</code> (containerd), <code>k3s://</code> {t("e, no Windows,")} <code>wsl://Ubuntu</code> (
          {t("uma distribuição do WSL2, sem SSH")}).
        </p>
      </div>
      <div className="field">
        <label htmlFor="hf-socket">
          {t("Socket remoto")} <span className="opt">{t("opcional")}</span>
        </label>
        <input id="hf-socket" placeholder="/var/run/docker.sock" autoComplete="off" spellCheck={false} value={socket} onChange={(e) => setSocket(e.target.value)} />
        <p className="hint">
          Podman rootless: <code>/run/user/1000/podman/podman.sock</code>
        </p>
      </div>
      <div className="field">
        <label htmlFor="hf-compose">Comando compose</label>
        <select id="hf-compose" value={compose} onChange={(e) => setCompose(e.target.value)}>
          <option value="">{t("Automático")}</option>
          <option value="docker compose">docker compose</option>
          <option value="podman compose">podman compose</option>
          <option value="docker-compose">docker-compose</option>
          <option value="podman-compose">podman-compose</option>
        </select>
      </div>
      {!hst && groups && groups.folders.length ? (
        <div className="field">
          <label>
            {t("Pasta")} <span className="opt">{t("opcional")}</span>
          </label>
          <FolderSelect g={groups} value={folder} onChange={setFolder} label={t("Pasta")} />
        </div>
      ) : null}
      {err ? <p className="flash flash-err">{err}</p> : null}
    </Modal>
  );
}
