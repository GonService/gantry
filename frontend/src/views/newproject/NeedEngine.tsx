import { useEffect } from "react";
import { loadLocal } from "@/lib/store";
import { t } from "@/lib/i18n";
import { LocalEngines } from "@/shell/localEngines";

// Criar projeto on a computer with no container engine yet: the projects
// (containers and VMs) run through one, so this step installs or starts it
// right here. As soon as the engine shows up, the wizard takes its place.
export function NeedEngine({ href, name }: { href: string; name: string }) {
  useEffect(() => {
    void loadLocal();
  }, []);
  return (
    <div>
      <div className="page-head">
        <div className="page-title">
          <nav className="crumbs" aria-label={t("Caminho")}>
            <a href={href}>{name}</a>
            <span aria-hidden="true">/</span>
            <span>{t("Projetos")}</span>
          </nav>
          <h1>{t("Criar projeto")}</h1>
          <div className="sub">{t("Primeiro, um engine de containers nesta máquina")}</div>
        </div>
        <div className="actions">
          <a className="btn btn-ghost" href={href}>
            {t("Cancelar")}
          </a>
        </div>
      </div>
      <div className="panel panel-pad" style={{ marginBottom: 16 }}>
        <p style={{ margin: 0 }}>
          {t("Os projetos do Gantry rodam num engine de containers (Podman, Docker, OrbStack…), inclusive os que só têm VMs. Instale ou inicie um abaixo: quando ele estiver rodando, a criação do projeto continua daqui.")}
        </p>
        <p className="muted small" style={{ marginBottom: 0 }}>
          {t("Prefere usar outro computador? Adicione um servidor por SSH na barra lateral e crie o projeto lá.")}
        </p>
      </div>
      <LocalEngines />
    </div>
  );
}
