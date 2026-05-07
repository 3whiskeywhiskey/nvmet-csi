{
  description = "nvmet-csi development shell";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = {nixpkgs, ...}: let
    systems = [
      "aarch64-darwin"
      "aarch64-linux"
      "x86_64-darwin"
      "x86_64-linux"
    ];

    forEachSystem = nixpkgs.lib.genAttrs systems;
  in {
    devShells = forEachSystem (system: let
      pkgs = import nixpkgs {inherit system;};
      agentShell = pkgs.mkShell {
        name = "nvmet-csi-dev";

        packages = with pkgs; [
          bash
          bc
          coreutils
          findutils
          gawk
          gcc
          git
          gnugrep
          gnused
          go_1_25
          golangci-lint
          gotools
          gnumake
          kubernetes-helm
        ];

        shellHook = ''
          export GOPATH="$HOME/go"
          export PATH="$GOPATH/bin:$PATH"
        '';
      };
    in {
      default = agentShell;
      agent = agentShell;
    });
  };
}
