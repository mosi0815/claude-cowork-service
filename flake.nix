{
  description = "Native Linux backend for Claude Desktop's Cowork feature";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      supportedSystems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
    in
    {
      packages = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          claude-cowork-service = pkgs.callPackage ./packaging/nix/package.nix { };
          default = self.packages.${system}.claude-cowork-service;
        }
      );

      checks = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          module-eval = pkgs.callPackage ./packaging/nix/module-test.nix {
            inherit nixpkgs self;
          };
        }
      );

      nixosModules.default = import ./packaging/nix/module.nix self;
    };
}
