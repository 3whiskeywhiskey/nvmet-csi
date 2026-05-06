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
    in {
      default = pkgs.mkShell {
        name = "nvmet-csi-dev";

        packages = with pkgs; [
          bc
          git
          go_1_25
          golangci-lint
          gotools
          gnumake
        ];

        shellHook = ''
          export GOPATH="$HOME/go"
          export PATH="$GOPATH/bin:$PATH"
        '';
      };
    });
  };
}
