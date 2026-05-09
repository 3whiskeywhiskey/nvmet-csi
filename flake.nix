{
  description = "nvmet-csi development shell and OCI image";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = {self, nixpkgs, ...}: let
    systems = [
      "aarch64-darwin"
      "aarch64-linux"
      "x86_64-darwin"
      "x86_64-linux"
    ];

    forEachSystem = nixpkgs.lib.genAttrs systems;

    sourceRepository = "https://gitea.whiskey.works/whiskey-works/nvmet-csi";
    goModuleVendorHash = "sha256-G0oe2n5AJav/uvuqsEe/BCWmXhB25IS91uYEzLk/8Ic=";
    driverVersion = "0.10.0";
    gitCommit = self.rev or self.dirtyRev or self.shortRev or self.dirtyShortRev or "unknown";
    rawBuildDate = self.lastModifiedDate or "unknown";
    buildDate =
      if builtins.stringLength rawBuildDate >= 14
      then "${builtins.substring 0 4 rawBuildDate}-${builtins.substring 4 2 rawBuildDate}-${builtins.substring 6 2 rawBuildDate}T${builtins.substring 8 2 rawBuildDate}:${builtins.substring 10 2 rawBuildDate}:${builtins.substring 12 2 rawBuildDate}Z"
      else rawBuildDate;

    mkPkgs = system: import nixpkgs {inherit system;};
  in {
    packages = forEachSystem (system: let
      pkgs = mkPkgs system;

      nvmetCSI = pkgs.buildGoModule {
        pname = "nvmet-csi";
        version = driverVersion;
        src = self;
        subPackages = ["cmd/nvmet-csi-plugin"];
        vendorHash = goModuleVendorHash;
        ldflags = [
          "-s"
          "-w"
          "-X gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/driver.version=${driverVersion}"
          "-X gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/driver.gitCommit=${gitCommit}"
          "-X gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/driver.buildDate=${buildDate}"
        ];
      };

      nvmetCSIImage = pkgs.dockerTools.buildLayeredImage {
        name = "nvmet-csi";
        tag = "nix";
        architecture = "amd64";
        contents = [
          nvmetCSI
          pkgs.bash
          pkgs.cacert
          pkgs.coreutils
          pkgs.e2fsprogs
          pkgs.findutils
          pkgs.gnugrep
          pkgs.gnused
          pkgs.lsof
          pkgs.nvme-cli
          pkgs.openssh
          pkgs.util-linux
          pkgs.xfsprogs
        ];
        extraCommands = ''
          mkdir -p csi etc/nvmet-csi/ssh-key var/lib/kubelet/plugins/nvmet.csi.whiskey.works var/lib/kubelet/pods
        '';
        config = {
          Entrypoint = ["/bin/nvmet-csi-plugin"];
          Env = [
            "PATH=/bin"
            "SSL_CERT_FILE=/etc/ssl/certs/ca-bundle.crt"
          ];
          Labels = {
            "org.opencontainers.image.source" = sourceRepository;
            "org.opencontainers.image.description" = "CSI driver for NVMe/TCP targets backed by Linux nvmet or RouterOS/RDS";
            "org.opencontainers.image.licenses" = "Apache-2.0";
            "org.opencontainers.image.version" = driverVersion;
            "org.opencontainers.image.revision" = gitCommit;
            "org.opencontainers.image.created" = buildDate;
          };
        };
      };
    in
      if system == "x86_64-linux"
      then {
        default = nvmetCSI;
        nvmet-csi = nvmetCSI;
        nvmet-csi-image = nvmetCSIImage;
      }
      else {});

    devShells = forEachSystem (system: let
      pkgs = mkPkgs system;
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
          skopeo
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
