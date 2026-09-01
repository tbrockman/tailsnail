{
  description = "tailsnail — a Tailscale-native peer-to-peer terminal Snake game";

  inputs = {
    # Pinned to unstable because tailscale.com v1.102 requires Go >= 1.26.6,
    # which the stable channel does not carry yet.
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};

        version = "0.1.0";
        rev = self.shortRev or self.dirtyShortRev or "unknown";

        tsnail = pkgs.buildGoModule {
          pname = "tsnail";
          inherit version;
          src = pkgs.lib.cleanSource ./.;

          vendorHash = "sha256-5LLAEt/48QFiBRINkXOxB0erSNmWSvz+gz22Hdnchrk=";

          subPackages = [ "cmd/tsnail" ];

          # Production users have no use for a symbol table or DWARF. These
          # remove information, not code, so they cannot change behaviour —
          # unlike Tailscale's ts_omit_ feature tags, which are deliberately
          # not used here. See scripts/build-release.sh.
          ldflags = [
            "-s"
            "-w"
            "-X github.com/tbrockman/tailsnail/internal/version.Version=${version}"
            "-X github.com/tbrockman/tailsnail/internal/version.Commit=${rev}"
          ];

          # The suite is pure Go with no network dependencies, so it is cheap
          # to run as part of every build.
          doCheck = true;

          meta = with pkgs.lib; {
            description = "Peer-to-peer terminal Snake played over your tailnet";
            homepage = "https://github.com/tbrockman/tailsnail";
            license = licenses.mit;
            mainProgram = "tsnail";
            platforms = platforms.unix;
          };
        };
      in
      {
        packages = {
          default = tsnail;
          tsnail = tsnail;
        };

        apps.default = {
          type = "app";
          program = "${tsnail}/bin/tsnail";
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools       # goimports
            go-tools      # staticcheck
            delve
            git
          ];

          shellHook = ''
            echo "tailsnail dev shell — go $(go version | cut -d' ' -f3)"
            echo "  go test ./...      run the suite"
            echo "  go run ./cmd/tsnail   launch the TUI"
          '';
        };

        formatter = pkgs.nixpkgs-fmt;
      });
}
