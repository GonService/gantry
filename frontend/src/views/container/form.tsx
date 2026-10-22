// The advanced form to create, edit (recreate) or duplicate a container,
// close to Portainer's.
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { gantry } from "@/lib/gantry";
import { ctrHref, enc, hostHref, RT_NAME, rtQuery, type Host } from "@/lib/app";
import { ErrorBox, Loading } from "@/components/ui";
import { engineChoices } from "@/views/host/obs";
import { EnvEditor, envRows, RowsEditor, type Row } from "./rows";
import { t } from "@/lib/i18n";

/* eslint-disable @typescript-eslint/no-explicit-any */
type Spec = {
  name: string;
  image: string;
  cmd: string[];
  entrypoint: string[];
  workingDir: string;
  user: string;
  tty: boolean;
  openStdin: boolean;
  env: string[];
  labels: Record<string, string>;
  ports: string[];
  publishAll: boolean;
  volumes: string[];
  tmpfs: string[];
  network: string;
  ipv4: string;
  hostname: string;
  domainname: string;
  macAddress: string;
  dns: string[];
  extraHosts: string[];
  restart: string;
  restartMax: number;
  privileged: boolean;
  init: boolean;
  readOnly: boolean;
  capAdd: string[];
  capDrop: string[];
  devices: string[];
  sysctls: Record<string, string>;
  memory: number;
  memoryReservation: number;
  cpus: number;
  shmSize: number;
  logDriver: string;
  logOpts: Record<string, string>;
};

const blankSpec = (): Spec => ({
  name: "",
  image: "",
  cmd: [],
  entrypoint: [],
  workingDir: "",
  user: "",
  tty: false,
  openStdin: false,
  env: [],
  labels: {},
  ports: [],
  publishAll: false,
  volumes: [],
  tmpfs: [],
  network: "",
  ipv4: "",
  hostname: "",
  domainname: "",
  macAddress: "",
  dns: [],
  extraHosts: [],
  restart: "no",
  restartMax: 0,
  privileged: false,
  init: false,
  readOnly: false,
  capAdd: [],
  capDrop: [],
  devices: [],
  sysctls: {},
  memory: 0,
  memoryReservation: 0,
  cpus: 0,
  shmSize: 0,
  logDriver: "",
  logOpts: {},
});

// Fields for the next new-container form (from "Usar" on an image).
let prefill: Partial<Spec> | null = null;
export const setPrefill = (p: Partial<Spec>) => {
  prefill = p;
};

// Sizes as Docker writes them: 512m, 2g.
export function parseSize(s: string): number {
  let v = String(s || "")
    .trim()
    .toLowerCase();
  if (!v) return 0;
  v = v.replace(/i?b$/, "");
  let mul = 1;
  const u = v.slice(-1);
  if (u === "k") mul = 1024;
  else if (u === "m") mul = 1 << 20;
  else if (u === "g") mul = 1 << 30;
  if (mul > 1) v = v.slice(0, -1);
  const n = Number(v);
  if (!isFinite(n) || n < 0 || v === "") throw new Error(t("Tamanho inválido: \"{size}\" (use 512m ou 2g).", { size: s }));
  return Math.round(n * mul);
}
function fmtSize(n: number) {
  if (!n) return "";
  if (n % (1 << 30) === 0) return n / (1 << 30) + "g";
  if (n % (1 << 20) === 0) return n / (1 << 20) + "m";
  if (n % 1024 === 0) return n / 1024 + "k";
  return String(n);
}
export function joinCmd(arr: string[] | undefined) {
  return (arr || []).map((a) => (a === "" || /[\s"'\\$]/.test(a) ? (a.includes("'") ? '"' + a + '"' : "'" + a + "'") : a)).join(" ");
}
// splitCmd splits a command line the way a shell would for simple cases.
export function splitCmd(s: string): string[] {
  const out: string[] = [];
  let cur = "",
    q = "",
    any = false;
  for (const ch of s) {
    if (q) {
      if (ch === q) q = "";
      else cur += ch;
      continue;
    }
    if (ch === '"' || ch === "'") {
      q = ch;
      any = true;
      continue;
    }
    if (/\s/.test(ch)) {
      if (cur || any) out.push(cur);
      cur = "";
      any = false;
      continue;
    }
    cur += ch;
  }
  if (cur || any) out.push(cur);
  return out;
}

function splitPortSpec(p: string): Row {
  let proto = "tcp";
  const i = p.lastIndexOf("/");
  if (i > 0) {
    proto = p.slice(i + 1);
    p = p.slice(0, i);
  }
  const parts = p.split(":");
  if (parts.length === 1) return ["", "", parts[0], proto];
  if (parts.length === 2) return ["", parts[0], parts[1], proto];
  return [parts.slice(0, -2).join(":"), parts[parts.length - 2], parts[parts.length - 1], proto];
}
function splitVolume(v: string): Row {
  const parts = v.split(":");
  let mode = "rw";
  if (parts.length > 2 && /^(ro|rw|z|Z|ro,.*|rw,.*)$/.test(parts[parts.length - 1])) mode = parts.pop()!.startsWith("ro") ? "ro" : "rw";
  if (parts.length === 1) return ["", parts[0], mode];
  return [parts[0], parts.slice(1).join(":"), mode];
}
const kvRows = (m: Record<string, string>): Row[] =>
  Object.entries(m || {})
    .sort()
    .map(([k, v]) => [k, v]);
const splitAt = (x: string, c: string): Row => {
  const i = x.indexOf(c);
  return i < 0 ? [x, ""] : [x.slice(0, i), x.slice(i + 1)];
};

type Model = {
  name: string;
  image: string;
  pull: boolean;
  start: boolean;
  rt: string;
  cmd: string;
  entrypoint: string;
  workingDir: string;
  user: string;
  console: string;
  ports: Row[];
  publishAll: boolean;
  volumes: Row[];
  tmpfs: Row[];
  network: string;
  ipv4: string;
  hostname: string;
  domainname: string;
  macAddress: string;
  dns: string;
  extraHosts: Row[];
  env: Row[];
  labels: Row[];
  restart: string;
  restartMax: string;
  memory: string;
  memoryReservation: string;
  cpus: string;
  shmSize: string;
  privileged: boolean;
  init: boolean;
  readOnly: boolean;
  capAdd: string;
  capDrop: string;
  devices: Row[];
  sysctls: Row[];
  logDriver: string;
  logOpts: Row[];
};

function toModel(sp: Spec, rt: string): Model {
  return {
    name: sp.name,
    image: sp.image,
    pull: false,
    start: true,
    rt,
    cmd: joinCmd(sp.cmd),
    entrypoint: joinCmd(sp.entrypoint),
    workingDir: sp.workingDir,
    user: sp.user,
    console: sp.tty && sp.openStdin ? "it" : sp.tty ? "t" : sp.openStdin ? "i" : "",
    ports: sp.ports.map(splitPortSpec),
    publishAll: sp.publishAll,
    volumes: sp.volumes.map(splitVolume),
    tmpfs: sp.tmpfs.map((x) => splitAt(x, ":")),
    network: sp.network,
    ipv4: sp.ipv4,
    hostname: sp.hostname,
    domainname: sp.domainname,
    macAddress: sp.macAddress,
    dns: sp.dns.join(", "),
    extraHosts: sp.extraHosts.map((x) => splitAt(x, ":")),
    env: envRows(sp.env),
    labels: kvRows(sp.labels),
    restart: sp.restart || "no",
    restartMax: sp.restartMax ? String(sp.restartMax) : "",
    memory: fmtSize(sp.memory),
    memoryReservation: fmtSize(sp.memoryReservation),
    cpus: sp.cpus ? String(sp.cpus) : "",
    shmSize: fmtSize(sp.shmSize),
    privileged: sp.privileged,
    init: sp.init,
    readOnly: sp.readOnly,
    capAdd: sp.capAdd.join(", "),
    capDrop: sp.capDrop.join(", "),
    devices: sp.devices.map((d) => {
      const p = d.split(":");
      return [p[0] || "", p[1] || "", p[2] || ""];
    }),
    sysctls: kvRows(sp.sysctls),
    logDriver: sp.logDriver,
    logOpts: kvRows(sp.logOpts),
  };
}

const customNet = (n: string) => !!n && !["bridge", "host", "none", "default"].includes(n) && !n.startsWith("container:");

function toSpec(m: Model): Spec {
  const nonEmpty = (rows: Row[]) => rows.filter((row) => row.some((v) => v && String(v).trim()));
  const obj = (rows: Row[]) => Object.fromEntries(nonEmpty(rows).map(([k, v]) => [k.trim(), v]));
  const list = (s: string) =>
    String(s || "")
      .split(/[,\s]+/)
      .map((x) => x.trim())
      .filter(Boolean);
  const out: Spec = {
    name: m.name.trim(),
    image: m.image.trim(),
    cmd: splitCmd(m.cmd || ""),
    entrypoint: splitCmd(m.entrypoint || ""),
    workingDir: (m.workingDir || "").trim(),
    user: (m.user || "").trim(),
    tty: m.console.includes("t"),
    openStdin: m.console.includes("i"),
    env: nonEmpty(m.env).map(([k, v]) => k.trim() + "=" + (v || "")),
    labels: obj(m.labels),
    ports: nonEmpty(m.ports).map(([ip, hp, cp, pr]) => {
      if (!cp) throw new Error(t("Informe a porta do container em todas as portas."));
      return (ip ? ip + ":" : "") + (hp || ip ? (hp || "") + ":" : "") + cp + "/" + (pr || "tcp");
    }),
    publishAll: m.publishAll,
    volumes: nonEmpty(m.volumes).map(([src, dst, md]) => {
      if (!dst) throw new Error(t("Informe o destino de todos os volumes."));
      return (src ? src + ":" : "") + dst + (md === "ro" ? ":ro" : "");
    }),
    tmpfs: nonEmpty(m.tmpfs).map(([p, o]) => p + (o ? ":" + o : "")),
    network: m.network,
    ipv4: customNet(m.network) ? (m.ipv4 || "").trim() : "",
    hostname: (m.hostname || "").trim(),
    domainname: (m.domainname || "").trim(),
    macAddress: (m.macAddress || "").trim(),
    dns: list(m.dns),
    extraHosts: nonEmpty(m.extraHosts).map(([n, ip]) => n.trim() + ":" + (ip || "").trim()),
    restart: m.restart === "no" ? "" : m.restart,
    restartMax: m.restart === "on-failure" ? Number(m.restartMax) || 0 : 0,
    privileged: m.privileged,
    init: m.init,
    readOnly: m.readOnly,
    capAdd: list(m.capAdd).map((x) => x.toUpperCase()),
    capDrop: list(m.capDrop).map((x) => x.toUpperCase()),
    devices: nonEmpty(m.devices).map((d) =>
      d
        .filter((x, i) => x || i === 0)
        .join(":")
        .replace(/:+$/, ""),
    ),
    sysctls: obj(m.sysctls),
    memory: parseSize(m.memory),
    memoryReservation: parseSize(m.memoryReservation),
    cpus: m.cpus ? Number(String(m.cpus).replace(",", ".")) : 0,
    shmSize: parseSize(m.shmSize),
    logDriver: (m.logDriver || "").trim(),
    logOpts: obj(m.logOpts),
  };
  if (!out.image) throw new Error(t("Informe a imagem."));
  if (!isFinite(out.cpus) || out.cpus < 0) throw new Error(t("CPUs inválido."));
  if (out.hostname && out.network === "host") out.hostname = "";
  return out;
}

export type FormProps = { host: string; mode: "novo" | "editar" | "duplicar"; rt: string; id: string };

export function ContainerForm({ host, mode, rt, id }: FormProps) {
  const [data, setData] = useState<{ sp: Spec; ins: any } | null>(() => {
    if (mode !== "novo") return null;
    const sp = { ...blankSpec(), ...(prefill || {}) };
    prefill = null;
    return { sp, ins: null };
  });
  const [err, setErr] = useState("");
  useEffect(() => {
    if (mode === "novo") return;
    api<{ spec: Spec; inspect: any }>("/hosts/" + enc(host) + "/containers/" + enc(id) + rtQuery(rt))
      .then((d) => {
        const sp: Spec = { ...blankSpec(), ...d.spec };
        for (const k of ["cmd", "entrypoint", "env", "ports", "volumes", "tmpfs", "dns", "extraHosts", "capAdd", "capDrop", "devices"] as const) (sp as any)[k] = (sp as any)[k] || [];
        for (const k of ["labels", "sysctls", "logOpts"] as const) (sp as any)[k] = (sp as any)[k] || {};
        if (!sp.restart) sp.restart = "no";
        if (mode === "duplicar") {
          sp.name = (sp.name || "container") + "-copia";
          delete sp.labels["com.docker.compose.container-number"];
        }
        setData({ sp, ins: d.inspect });
      })
      .catch((e) => setErr((e as Error).message));
  }, [host, mode, rt, id]);
  const hst = gantry.hostByName(host) as Host | undefined;
  if (err) return <ErrorBox>{err}</ErrorBox>;
  if (!data || !hst) return <Loading>{t("Lendo o container…")}</Loading>;
  return <Form hst={hst} mode={mode} rt={rt} sp={data.sp} ins={data.ins} />;
}

const TABS: [string, string][] = [
  ["comando", t("Comando")],
  ["portas", t("Portas")],
  ["volumes", t("Volumes")],
  ["rede", t("Rede")],
  ["env", t("Variáveis")],
  ["labels", t("Labels")],
  ["reinicio", t("Reinício")],
  ["recursos", t("Recursos")],
  ["runtime", t("Segurança")],
];

function Form({ hst, mode, rt, sp, ins }: { hst: Host; mode: FormProps["mode"]; rt: string; sp: Spec; ins: any }) {
  const [m, setM] = useState<Model>(() => {
    const x = toModel(sp, rt || "");
    if (ins && ins.State && mode === "editar") x.start = !!ins.State.Running;
    return x;
  });
  const set = <K extends keyof Model>(k: K, v: Model[K]) => setM((s) => ({ ...s, [k]: v }));
  const [tab, setTab] = useState("comando");
  const [nets, setNets] = useState<string[]>([]);
  const [vols, setVols] = useState<string[]>([]);
  const [engines, setEngines] = useState<[string, string][]>([]);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const id: string | undefined = ins?.Id;
  useEffect(() => {
    const q = rtQuery(m.rt);
    api<{ name: string }[]>("/hosts/" + enc(hst.name) + "/res/networks" + q)
      .then((l) => setNets(l.map((n) => n.name)))
      .catch(() => setNets([]));
    api<{ name: string }[]>("/hosts/" + enc(hst.name) + "/res/volumes" + q)
      .then((l) => setVols(l.map((v) => v.name)))
      .catch(() => {});
  }, [m.rt, hst.name]);
  useEffect(() => {
    if (mode === "editar") return;
    engineChoices(hst.name).then((list) => {
      const others = list.filter((e) => e.runtime !== hst.runtime);
      if (others.length) setEngines([["", t("o deste host ({rt})", { rt: hst.runtime || "docker" })], ...others.map((e) => [e.runtime, RT_NAME[e.runtime] || e.runtime] as [string, string])]);
    });
  }, [hst.name]); // eslint-disable-line react-hooks/exhaustive-deps

  const field = (label: string, input: React.ReactNode, hint?: string | null, opt?: boolean) => (
    <div className="field">
      <label>
        {label}
        {opt ? <span className="opt"> {t("opcional")}</span> : null}
      </label>
      {input}
      {hint ? <p className="hint">{hint}</p> : null}
    </div>
  );
  type TextKey = { [K in keyof Model]: Model[K] extends string ? K : never }[keyof Model];
  type BoolKey = { [K in keyof Model]: Model[K] extends boolean ? K : never }[keyof Model];
  type RowsKey = { [K in keyof Model]: Model[K] extends Row[] ? K : never }[keyof Model];
  const text = (k: TextKey, ph: string, extra: React.InputHTMLAttributes<HTMLInputElement> = {}) => (
    <input value={m[k] || ""} placeholder={ph} spellCheck={false} autoComplete="off" onChange={(e) => set(k, e.target.value)} {...extra} />
  );
  const check = (k: BoolKey, label: string, hint?: string) => (
    <label className="check" title={hint}>
      <input type="checkbox" checked={!!m[k]} onChange={(e) => set(k, e.target.checked)} /> {label}
    </label>
  );
  const select = (k: TextKey, options: [string, string][]) => (
    <select value={m[k]} onChange={(e) => set(k, e.target.value)}>
      {options.map(([v, l]) => (
        <option key={v} value={v}>
          {l}
        </option>
      ))}
    </select>
  );
  const rows = (k: RowsKey, cols: Parameters<typeof RowsEditor>[0]["cols"], add: string) => <RowsEditor rows={m[k]} cols={cols} addLabel={add} onChange={(r) => set(k, r)} />;
  const netNames = [...new Set(["bridge", "host", "none", ...nets])];
  if (m.network && !netNames.includes(m.network)) netNames.push(m.network);

  const panes: Record<string, React.ReactNode> = {
    comando: (
      <>
        {field(t("Comando"), text("cmd", 'nginx -g "daemon off;"'), t("Vazio usa o da imagem."), true)}
        {field("Entrypoint", text("entrypoint", "/docker-entrypoint.sh"), t("Vazio usa o da imagem."), true)}
        <div className="cols2">
          {field(t("Diretório de trabalho"), text("workingDir", "/app"), null, true)}
          {field(t("Usuário"), text("user", "1000:1000"), null, true)}
        </div>
        {field(
          t("Console"),
          select("console", [
            ["", t("nenhum")],
            ["it", t("interativo e TTY (-it)")],
            ["t", t("só TTY (-t)")],
            ["i", t("só interativo (-i)")],
          ]),
          t("Para imagens como alpine ou ubuntu ficarem rodando sem comando, use interativo e TTY."),
        )}
      </>
    ),
    portas: (
      <>
        {rows(
          "ports",
          [
            { ph: t("IP do host"), label: t("IP do host"), w: "1.2" },
            { ph: t("porta host"), label: t("Porta no host"), w: "1" },
            { ph: t("porta ctr"), label: t("Porta no container"), w: "1" },
            {
              label: t("Protocolo"),
              w: ".7",
              options: [
                ["tcp", "TCP"],
                ["udp", "UDP"],
                ["sctp", "SCTP"],
              ],
            },
          ],
          t("porta"),
        )}
        <p className="hint">{t("IP vazio publica em todas as interfaces. Porta do host vazia: o engine escolhe uma livre.")}</p>
        {check("publishAll", t("Publicar todas as portas expostas pela imagem em portas aleatórias (-P)"))}
      </>
    ),
    volumes: (
      <>
        <datalist id="cf-vols">
          {vols.map((v) => (
            <option key={v} value={v} />
          ))}
        </datalist>
        {rows(
          "volumes",
          [
            { ph: t("volume ou /caminho/no/host"), label: t("Origem"), w: "2", list: "cf-vols" },
            { ph: t("/no/container"), label: t("Destino"), w: "2" },
            {
              label: t("Acesso"),
              w: ".9",
              options: [
                ["rw", t("leitura e escrita")],
                ["ro", t("só leitura")],
              ],
            },
          ],
          t("volume"),
        )}
        <p className="hint">{t("Um nome cria (ou usa) um volume; um caminho começando com / monta a pasta do host. Origem vazia: volume anônimo.")}</p>
        <h3 className="sub">{t("tmpfs (em memória)")}</h3>
        {rows(
          "tmpfs",
          [
            { ph: "/tmp", label: t("Caminho"), w: "2" },
            { ph: "size=64m,mode=1777", label: t("Opções"), w: "2" },
          ],
          "tmpfs",
        )}
      </>
    ),
    rede: (
      <>
        <div className="cols2">
          {field(
            t("Rede"),
            <select value={m.network} onChange={(e) => set("network", e.target.value)}>
              <option value="">{t("padrão do engine")}</option>
              {netNames.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>,
            t("host e none compartilham ou desligam a rede."),
          )}
          {field(t("IP fixo"), text("ipv4", "172.30.0.10", { disabled: !customNet(m.network) }), t("Só em redes criadas por você, com sub-rede definida."), true)}
        </div>
        <div className="cols2">
          {field("Hostname", text("hostname", "web"), null, true)}
          {field(t("Domínio"), text("domainname", "lab.local"), null, true)}
        </div>
        <div className="cols2">
          {field("MAC", text("macAddress", "02:42:ac:11:00:02"), null, true)}
          {field("DNS", text("dns", "1.1.1.1, 8.8.8.8"), t("Separados por vírgula."), true)}
        </div>
        <h3 className="sub">{t("Entradas extras no /etc/hosts")}</h3>
        {rows(
          "extraHosts",
          [
            { ph: t("nome"), label: t("Nome"), w: "1" },
            { ph: t("IP ou host-gateway"), label: "IP", w: "1" },
          ],
          t("entrada"),
        )}
      </>
    ),
    env: <EnvEditor rows={m.env} onChange={(r) => set("env", r)} />,
    labels: rows(
      "labels",
      [
        { ph: t("nome"), label: t("Nome"), w: "1" },
        { ph: t("valor"), label: t("Valor"), w: "2" },
      ],
      t("label"),
    ),
    reinicio: (
      <div className="cols2">
        {field(
          t("Política"),
          select("restart", [
            ["no", t("nunca")],
            ["unless-stopped", t("sempre, a menos que eu pare")],
            ["always", t("sempre")],
            ["on-failure", t("quando falhar")],
          ]),
        )}
        {field(t("Tentativas"), text("restartMax", "0", { type: "number", min: "0", disabled: m.restart !== "on-failure" }), t("Só para \"quando falhar\"; 0 é sem limite."), true)}
      </div>
    ),
    recursos: (
      <>
        <div className="cols2">
          {field(t("Limite de memória"), text("memory", "512m"), t("Ex.: 512m, 2g. Vazio: sem limite."), true)}
          {field(t("Reserva de memória"), text("memoryReservation", "256m"), t("Limite flexível, aplicado quando falta memória."), true)}
        </div>
        <div className="cols2">
          {field("CPUs", text("cpus", "1.5", { inputMode: "decimal" }), t("Ex.: 0.5 é meia CPU."), true)}
          {field("/dev/shm", text("shmSize", "64m"), null, true)}
        </div>
      </>
    ),
    runtime: (
      <>
        <div className="checks">
          {check("privileged", t("Privilegiado"), t("Acesso total ao host: evite fora de testes."))}
          {check("init", t("Init (repassa sinais e recolhe zumbis)"))}
          {check("readOnly", t("Sistema de arquivos só leitura"))}
        </div>
        <div className="cols2">
          {field(t("Adicionar capacidades"), text("capAdd", "NET_ADMIN, SYS_TIME"), t("Separadas por vírgula. Ex.: NET_ADMIN, SYS_PTRACE, SYS_ADMIN."), true)}
          {field(t("Remover capacidades"), text("capDrop", "ALL"), null, true)}
        </div>
        <h3 className="sub">{t("Dispositivos")}</h3>
        {rows(
          "devices",
          [
            { ph: t("/dev/no/host"), label: t("No host"), w: "2" },
            { ph: t("/dev/no/container"), label: t("No container"), w: "2" },
            { ph: "rwm", label: t("Permissões"), w: ".7" },
          ],
          t("dispositivo"),
        )}
        <h3 className="sub">Sysctls</h3>
        {rows(
          "sysctls",
          [
            { ph: "net.ipv4.ip_forward", label: t("Chave"), w: "2" },
            { ph: "1", label: t("Valor"), w: "1" },
          ],
          "sysctl",
        )}
        <h3 className="sub">Logs</h3>
        {field(t("Driver de log"), text("logDriver", "json-file"), t("Vazio usa o padrão do engine."), true)}
        {rows(
          "logOpts",
          [
            { ph: "max-size", label: t("Opção"), w: "1" },
            { ph: "10m", label: t("Valor"), w: "1" },
          ],
          t("opção"),
        )}
      </>
    ),
  };
  const count = (k: string) => {
    const n = ({ portas: m.ports.length, volumes: m.volumes.length + m.tmpfs.length, env: m.env.length, labels: m.labels.length } as Record<string, number>)[k];
    return n ? " · " + n : "";
  };
  const submitLabel = mode === "editar" ? t("Salvar e recriar") : m.start ? t("Criar e iniciar") : t("Criar");
  const submit = async () => {
    setErr("");
    let spec: Spec;
    try {
      spec = toSpec(m);
    } catch (e) {
      setErr((e as Error).message);
      return;
    }
    setBusy(true);
    try {
      const q = rtQuery(m.rt);
      let res: { id: string; pulled?: boolean };
      if (mode === "editar") {
        res = await api("/hosts/" + enc(hst.name) + "/containers/" + enc(id!) + q, { method: "PUT", body: { spec, pull: m.pull, start: m.start } });
        gantry.toast(t("Container recriado"), "ok");
      } else {
        res = await api("/hosts/" + enc(hst.name) + "/containers" + q, { method: "POST", body: { ...spec, pull: m.pull, start: m.start } });
        gantry.toast(res.pulled ? t("Container criado (imagem baixada)") : t("Container criado"), "ok");
      }
      gantry.loadLabs(hst.name);
      location.hash = ctrHref(hst.name, m.rt, res.id);
    } catch (e) {
      setErr((e as Error).message);
    }
    setBusy(false);
  };
  const labels = ins?.Config?.Labels;
  const title = mode === "editar" ? t("Editar {name}", { name: sp.name }) : mode === "duplicar" ? t("Duplicar {name}", { name: ins ? ins.Name.replace(/^\//, "") : "" }) : t("Novo container");
  return (
    <div className="ctr-form">
      <div className="page-head">
        <div className="page-title">
          <nav className="crumbs" aria-label={t("Caminho")}>
            <a href={hostHref(hst.name)}>{hst.name}</a>
            <span aria-hidden="true">/</span>
            {id ? <a href={ctrHref(hst.name, rt, id)}>{sp.name || id.slice(0, 12)}</a> : <span>Containers</span>}
          </nav>
          <h1>{title}</h1>
        </div>
      </div>
      {mode === "editar" ? (
        <div className="flash flash-warn">
          {t("Salvar recria o container: ele é parado e trocado por um novo com estas opções. O que estiver fora de volumes se perde. Se algo der errado, o container atual é mantido (ou recriado como estava).")}
        </div>
      ) : null}
      {labels && labels["com.docker.compose.project"] ? (
        <div className="flash flash-warn">
          {t("Este container é do projeto \"{project}\". Quando o projeto subir de novo, o compose pode trocá-lo pelo que está no arquivo; para mudanças permanentes, edite a aba Arquivo do projeto.", { project: labels["com.docker.compose.project"] })}
        </div>
      ) : null}
      <div className="panel panel-pad cf-top">
        <div className="cols2">
          {field(t("Nome"), text("name", "web"), mode === "novo" ? t("Vazio: o engine escolhe um nome.") : null, mode === "novo")}
          {field(t("Imagem"), text("image", "nginx:alpine"), t("Sem registry, vem do Docker Hub."))}
        </div>
        {engines.length ? field("Engine", select("rt", engines), t("Onde o container vai rodar.")) : null}
        <div className="checks">
          {check("pull", t("Sempre baixar a imagem antes (pega a versão mais nova da tag)"))}
          {mode === "editar" ? check("start", t("Deixar rodando depois de recriar")) : check("start", t("Iniciar depois de criar"))}
        </div>
      </div>
      <div className="tabs" role="tablist" aria-label={t("Opções do container")}>
        {TABS.map(([k, label]) => (
          <button key={k} type="button" className="tab" role="tab" aria-selected={k === tab} onClick={() => setTab(k)}>
            {label + count(k)}
          </button>
        ))}
      </div>
      <div className="panel panel-pad">
        <div className="cf-pane">{panes[tab]}</div>
      </div>
      {err ? <p className="flash flash-err">{err}</p> : null}
      <div className="cf-actions">
        <a className="btn btn-ghost" href={id ? ctrHref(hst.name, rt, id) : hostHref(hst.name)}>
          {t("Cancelar")}
        </a>
        <button type="button" className="btn primary" disabled={busy} onClick={submit}>
          {busy ? (
            <>
              <span className="spin" />
              {mode === "editar" ? t("Recriando…") : t("Criando…")}
            </>
          ) : (
            submitLabel
          )}
        </button>
      </div>
    </div>
  );
}
