# Imagem base genérica para labs do Gantry.
#
# A imagem é sempre a mesma. O que muda por lab:
#   /etc/nixos/lab.nix            -> módulo NixOS do lab (o "template")
#   /etc/nixos/gantry-params.json -> parâmetros da instância (hostname etc.)
# Os dois chegam via cloud-init (write_files). O serviço gantry-apply detecta
# os arquivos e roda `nixos-rebuild switch`.
#
# Este arquivo também é copiado para dentro da VM (/etc/nixos/base.nix),
# então precisa ser autocontido: nada de caminhos relativos ao repo.
{
  config,
  lib,
  pkgs,
  modulesPath,
  ...
}:
let
  labFile = "/etc/nixos/lab.nix";
  paramsFile = "/etc/nixos/gantry-params.json";
  stateDir = "/var/lib/gantry";

  # Script de aplicação: idempotente, só rebuilda quando algo muda.
  # Deixa o estado em /var/lib/gantry para o Gantry consultar via SSH/agent.
  applyScript = pkgs.writeShellApplication {
    name = "gantry-apply";
    runtimeInputs = with pkgs; [
      coreutils
      # O nixos-rebuild chama o nix; um serviço do systemd não tem o nix no PATH.
      config.nix.package
      config.system.build.nixos-rebuild
    ];
    text = ''
      mkdir -p ${stateDir}

      if [ ! -f ${labFile} ]; then
        echo "sem ${labFile}, nada a aplicar"
        echo "idle" > ${stateDir}/status
        exit 0
      fi

      # O hash cobre o template (lab.nix) e os parâmetros da instância.
      want=$(cat ${labFile} ${paramsFile} 2>/dev/null | sha256sum | cut -d' ' -f1)
      have=$(cat ${stateDir}/applied.sha256 2>/dev/null || true)

      if [ "$want" = "$have" ]; then
        echo "lab já aplicado ($want)"
        echo "ok $want" > ${stateDir}/status
        exit 0
      fi

      echo "applying $want" > ${stateDir}/status
      if nixos-rebuild switch 2>&1 | tee ${stateDir}/last-apply.log; then
        echo "$want" > ${stateDir}/applied.sha256
        echo "ok $want" > ${stateDir}/status
      else
        echo "failed $want" > ${stateDir}/status
        exit 1
      fi
    '';
  };
in
{
  imports = [
    (modulesPath + "/profiles/qemu-guest.nix")
    # Define fileSystems, bootloader UEFI e o builder do qcow2
    # (config.system.build.image). Dentro da VM só é avaliado, nunca buildado.
    (modulesPath + "/virtualisation/disk-image.nix")
  ];

  # Parâmetros da instância, lidos pelo lab.nix via config.gantry.params.
  # Assim o mesmo lab.nix serve de template para várias VMs.
  options.gantry.params = lib.mkOption {
    type = lib.types.attrsOf lib.types.anything;
    default = { };
    description = "Parâmetros da instância injetados pelo Gantry (gantry-params.json).";
  };

  config = {
    system.stateVersion = "26.05";

    image = {
      format = "qcow2";
      efiSupport = true;
      baseName = "nixos-lab-base";
    };
    # Imagem mínima; o disco cresce no primeiro boot (growPartition + autoResize).
    virtualisation.diskSize = "auto";

    boot.loader.efi.canTouchEfiVariables = false;

    # Console serial: o Gantry pode mostrar o boot sem precisar de VNC.
    # (o último console= vira o principal)
    boot.kernelParams = [
      "console=tty0"
      (if pkgs.stdenv.hostPlatform.isAarch64 then "console=ttyAMA0" else "console=ttyS0")
    ];

    # --- Rede: DHCP via networkd, serve para user-net e para bridge ---
    networking.useNetworkd = true;
    networking.hostName = lib.mkDefault "nixos-lab";

    # --- Acesso: usuário gantry declarado aqui; a chave SSH vem do cloud-init ---
    users.users.gantry = {
      isNormalUser = true;
      extraGroups = [ "wheel" ];
    };
    security.sudo.wheelNeedsPassword = false;

    # --- Métricas (metrics: true no compose): o node_exporter na porta 9100,
    # que o Gantry encaminha para uma porta local do host ---
    services.prometheus.exporters.node = lib.mkIf (config.gantry.params.metrics or false) {
      enable = true;
      openFirewall = true;
    };

    services.openssh = {
      enable = true;
      settings = {
        PasswordAuthentication = false;
        PermitRootLogin = "no";
      };
    };

    # --- cloud-init: chaves, write_files e o network-config do Gantry ---
    # A rede fica com o cloud-init para reaproveitar o network-config v2 que o
    # Gantry já gera (IP fixo na rede do projeto quando lan: true). Os arquivos
    # dele (10-cloud-init-*.network) têm prioridade sobre o DHCP padrão do NixOS.
    services.cloud-init = {
      enable = true;
      network.enable = true;
      settings = {
        datasource_list = [
          "NoCloud"
          "None"
        ];
        # Quem manda no hostname é o Nix (gantry.params.hostname).
        preserve_hostname = true;
      };
    };

    # --- Nix dentro da VM, travado no mesmo nixpkgs que gerou a imagem ---
    # Sem channels: o rebuild usa a mesma revisão do nixpkgs, então tudo
    # que já está na imagem é reaproveitado sem download.
    nix = {
      channel.enable = false;
      nixPath = [
        "nixpkgs=${pkgs.path}"
        "nixos-config=/etc/nixos/configuration.nix"
      ];
      settings.experimental-features = [
        "nix-command"
        "flakes"
      ];
    };

    # O que o nixos-rebuild avalia dentro da VM: esta base + o lab injetado.
    environment.etc."nixos/base.nix".source = ./base.nix;
    environment.etc."nixos/configuration.nix".text = ''
      { lib, ... }:
      {
        imports = [ ./base.nix ]
          ++ lib.optional (builtins.pathExists ${labFile}) ${labFile};

        gantry.params = lib.mkIf (builtins.pathExists ${paramsFile})
          (lib.importJSON ${paramsFile});
      }
    '';

    # --- Aplicação da config do lab ---
    environment.systemPackages = [ applyScript ];

    systemd.services.gantry-apply = {
      description = "Aplica a config do lab injetada pelo Gantry";
      wantedBy = [ "multi-user.target" ];
      after = [
        "network-online.target"
        "cloud-final.service"
      ];
      wants = [ "network-online.target" ];
      # O nix.nixPath só vai para as sessões de login (sessionVariables); o
      # serviço precisa dele para o nixos-rebuild achar <nixpkgs/nixos> e a
      # configuration.nix.
      environment.NIX_PATH = lib.concatStringsSep ":" config.nix.nixPath;
      # Não reiniciar/parar este serviço durante o próprio switch.
      restartIfChanged = false;
      stopIfChanged = false;
      serviceConfig = {
        Type = "oneshot";
        ExecStart = lib.getExe applyScript;
      };
    };

    # Se o Gantry trocar os arquivos depois (via SSH/agent), aplica de novo.
    systemd.paths.gantry-apply = {
      wantedBy = [ "multi-user.target" ];
      pathConfig.PathChanged = [
        labFile
        paramsFile
      ];
    };

    # Avaliar o NixOS dentro da VM consome memória; evita OOM em VMs de 2 GB.
    zramSwap.enable = true;

    # Pacotes já na imagem (evitam download no primeiro apply).
    environment.defaultPackages = with pkgs; [
      git
      curl
      htop
    ];
  };
}
