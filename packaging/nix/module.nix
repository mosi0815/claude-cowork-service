flake:
{ config, lib, pkgs, ... }:

let
  cfg = config.services.claude-cowork;
in
{
  options.services.claude-cowork = {
    enable = lib.mkEnableOption "Claude Cowork Service (native Linux backend)";

    package = lib.mkOption {
      type = lib.types.package;
      default = flake.packages.${pkgs.system}.claude-cowork-service;
      description = "The claude-cowork-service package to use.";
    };

    extraPath = lib.mkOption {
      type = lib.types.listOf (lib.types.either lib.types.package lib.types.str);
      default = [];
      description = ''
        Extra packages or directories to add to the service PATH.
        The cowork service invokes the `claude` CLI internally, which must
        be reachable in the systemd service PATH.

        If Claude Code is installed via npm global:
          extraPath = [ pkgs.nodejs "/home/user/.npm-global" ];

        If Claude Code is available as a Nix package:
          extraPath = [ pkgs.claude-code ];
      '';
      example = lib.literalExpression ''[ pkgs.nodejs "/home/user/.npm-global" ]'';
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.user.services.claude-cowork = {
      description = "Claude Cowork Service (native Linux backend)";
      after = [ "default.target" ];
      wantedBy = [ "default.target" ];
      path = cfg.extraPath;
      serviceConfig = {
        ExecStart = "${cfg.package}/bin/cowork-svc-linux";
        Restart = "on-failure";
        RestartSec = 5;
      };
    };

    environment.systemPackages = [ cfg.package ];
  };
}
