# NixOS module evaluation test
# Verifies that the module evaluates correctly and produces expected systemd config.
{ pkgs, nixpkgs, self, packageOverride ? null }:

let
  # Minimal config to satisfy NixOS module evaluation
  baseModules = [
    self.nixosModules.default
    {
      boot.loader.grub.device = "nodev";
      fileSystems."/" = { device = "none"; fsType = "tmpfs"; };
      system.stateVersion = "24.11";
    }
  ] ++ pkgs.lib.optionals (packageOverride != null) [{
    services.claude-cowork.package = packageOverride;
  }];

  # Evaluate with default settings (just enable the service)
  defaultEval = (nixpkgs.lib.nixosSystem {
    system = pkgs.system;
    modules = baseModules ++ [{
      services.claude-cowork.enable = true;
    }];
  }).config;

  defaultSvc = defaultEval.systemd.user.services.claude-cowork;

  # Check if the module exposes the extraPath option
  hasExtraPath = builtins.hasAttr "extraPath" defaultEval.services.claude-cowork;

  # Conditionally evaluate extraPath config
  extraPathSvc = if hasExtraPath then
    (nixpkgs.lib.nixosSystem {
      system = pkgs.system;
      modules = baseModules ++ [{
        services.claude-cowork = {
          enable = true;
          extraPath = [ pkgs.hello "/tmp/test-path" ];
        };
      }];
    }).config.systemd.user.services.claude-cowork
  else null;

  # Build extra-path test commands (empty string when feature absent)
  extraPathTests = if hasExtraPath then ''
    echo ""
    echo "--- extraPath config ---"

    run_test "path includes hello package" \
      '[[ "${toString extraPathSvc.path}" == *hello* ]]'

    run_test "path includes custom string dir" \
      '[[ "${toString extraPathSvc.path}" == */tmp/test-path* ]]'

    run_test "ExecStart still correct with extraPath" \
      '[[ "${extraPathSvc.serviceConfig.ExecStart}" == *cowork-svc-linux* ]]'
  '' else ''
    echo ""
    echo "--- extraPath config ---"
    echo "  SKIP: extraPath option not present (see PR #11)"
  '';

in pkgs.runCommand "nixos-module-eval-test" { } ''
  echo "=== NixOS Module Evaluation Tests ==="
  failures=0

  run_test() {
    local name="$1"; local cond="$2"
    if eval "$cond"; then
      echo "  PASS: $name"
    else
      echo "  FAIL: $name"
      failures=$((failures + 1))
    fi
  }

  echo ""
  echo "--- Default config ---"

  run_test "ExecStart contains cowork-svc-linux" \
    '[[ "${defaultSvc.serviceConfig.ExecStart}" == *cowork-svc-linux* ]]'

  run_test "Restart = on-failure" \
    '[[ "${defaultSvc.serviceConfig.Restart}" == "on-failure" ]]'

  run_test "RestartSec = 5" \
    '[[ "${toString defaultSvc.serviceConfig.RestartSec}" == "5" ]]'

  run_test "wantedBy default.target" \
    '[[ "${toString defaultSvc.wantedBy}" == *default.target* ]]'

  run_test "after default.target" \
    '[[ "${toString defaultSvc.after}" == *default.target* ]]'

  run_test "package in systemPackages" \
    '[[ "${toString defaultEval.environment.systemPackages}" == *claude-cowork-service* ]]'

  ${extraPathTests}

  echo ""
  if [ "$failures" -gt 0 ]; then
    echo "=== $failures test(s) FAILED ==="
    exit 1
  fi

  echo "=== All tests passed ==="
  mkdir -p $out
  echo "passed" > $out/result
''
