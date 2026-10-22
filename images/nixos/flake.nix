{
  description = "Imagem base NixOS para VMs do Gantry";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      mkBase =
        system:
        nixpkgs.lib.nixosSystem {
          modules = [
            ./base.nix
            { nixpkgs.hostPlatform = system; }
          ];
        };
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
    in
    {
      nixosConfigurations = nixpkgs.lib.genAttrs systems mkBase;

      # nix build .#packages.<sistema>.base-qcow2 -> result/nixos-lab-base.qcow2
      packages = nixpkgs.lib.genAttrs systems (system: {
        base-qcow2 = self.nixosConfigurations.${system}.config.system.build.image;
        default = self.packages.${system}.base-qcow2;
      });
    };
}
