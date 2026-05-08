{ pkgs ? import <nixpkgs> {} }:

pkgs.mkShell {
  name = "nvmet-csi-dev";

  buildInputs = with pkgs; [
    # Go toolchain
    go_1_24

    # Go development tools
    golangci-lint
    gotools  # includes goimports

    # Container tools
    docker
    docker-buildx

    # Kubernetes tools
    kubectl
    kubernetes-helm

    # SSH and networking
    openssh

    # Build tools
    gnumake
    git

    # Optional: CSI testing (if available)
    # csi-sanity would need to be built from source or added to nixpkgs
  ];

  shellHook = ''
    echo "nvmet-csi driver Development Environment"
    echo ""
    echo "Available tools:"
    echo "  - go $(go version | cut -d' ' -f3)"
    echo "  - golangci-lint $(golangci-lint --version 2>/dev/null | head -1 || echo 'installed')"
    echo "  - goimports (installed)"
    echo "  - kubectl $(kubectl version --client -o json 2>/dev/null | jq -r '.clientVersion.gitVersion' || echo 'installed')"
    echo "  - helm $(helm version --short 2>/dev/null || echo 'installed')"
    echo ""
    echo "Quick commands:"
    echo "  make build-local    - Build for local OS/arch"
    echo "  make verify         - Run all checks"
    echo "  make test           - Run unit tests"
    echo "  make docker         - Build Docker image"
    echo ""

    # Set up Go environment
    export GOPATH="$HOME/go"
    export PATH="$GOPATH/bin:$PATH"

    # Ensure go.work is not interfering (if it exists)
    if [ -f go.work ]; then
      echo "Warning: go.work file detected. Consider using 'go work use' if needed."
    fi
  '';
}
