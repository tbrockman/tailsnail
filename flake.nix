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

          # Tailscale carries a great deal a game has no use for; its ts_omit_
          # tags drop those features at compile time, taking about 15% off the
          # binary. Kept in step with scripts/build-release.sh, which is what
          # CI and the release workflow use.
          #
          # serve and acme are conspicuously absent: they cannot be omitted,
          # because tsnet itself references SetServeConfig and GetCertificate.
          # Omitting logtail also removes any path for uploading logs to
          # Tailscale's servers, which is the right default for a game.
          tags = [
            "ts_omit_aws" "ts_omit_kube" "ts_omit_bird" "ts_omit_synology"
            "ts_omit_cloud" "ts_omit_dbus" "ts_omit_networkmanager"
            "ts_omit_resolved" "ts_omit_systray" "ts_omit_desktop_sessions"
            "ts_omit_serviceclientprefs" "ts_omit_syspolicy" "ts_omit_sdnotify"
            "ts_omit_syslog" "ts_omit_tpm"
            "ts_omit_appconnectors" "ts_omit_captiveportal" "ts_omit_conn25"
            "ts_omit_drive" "ts_omit_taildrop" "ts_omit_tailnetlock"
            "ts_omit_ssh" "ts_omit_tap" "ts_omit_webclient"
            "ts_omit_relayserver" "ts_omit_outboundproxy" "ts_omit_portlist"
            "ts_omit_posture" "ts_omit_wakeonlan" "ts_omit_identityfederation"
            "ts_omit_oauthkey" "ts_omit_remoteconfig"
            "ts_omit_advertiseexitnode" "ts_omit_advertiseroutes"
            "ts_omit_useexitnode" "ts_omit_useroutes" "ts_omit_c2n"
            "ts_omit_logtail" "ts_omit_netlog" "ts_omit_runtimemetrics"
            "ts_omit_cli" "ts_omit_cliconndiag" "ts_omit_completion"
            "ts_omit_completion_scripts" "ts_omit_qrcodes"
            "ts_omit_clientupdate" "ts_omit_flashappliance"
            "ts_omit_hujsonconf"
            "ts_omit_debug" "ts_omit_debugeventbus" "ts_omit_debugportmapper"
            "ts_omit_doctor" "ts_omit_capture"
            "ts_omit_iptables" "ts_omit_linkspeed" "ts_omit_linuxdnsfight"
            "ts_omit_tundevstats"
          ];

          # Production users have no use for a symbol table or DWARF.
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
