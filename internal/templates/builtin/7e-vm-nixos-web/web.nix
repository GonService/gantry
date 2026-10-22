# Template de lab "web": nginx servindo uma página.
#
# O Gantry entrega este arquivo via cloud-init em /etc/nixos/lab.nix.
# É um módulo NixOS comum: qualquer opção do NixOS vale aqui.
# Valores por instância vêm de config.gantry.params (gantry-params.json).
{ config, pkgs, ... }:
let
  p = config.gantry.params;
  hostname = p.hostname or "web-01";
  port = p.httpPort or 80;
in
{
  networking.hostName = hostname;

  services.nginx = {
    enable = true;
    virtualHosts.default = {
      default = true;
      listen = [
        {
          addr = "0.0.0.0";
          inherit port;
        }
      ];
      root = pkgs.writeTextDir "index.html" ''
        <h1>${p.title or "Lab web"}</h1>
        <p>Host ${hostname}, config aplicada via cloud-init + nixos-rebuild.</p>
      '';
    };
  };

  networking.firewall.allowedTCPPorts = [ port ];
}
