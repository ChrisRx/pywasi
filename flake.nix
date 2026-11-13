{
  description = "pywasi: Python (with numpy) compiled to WASI modules for wasmtime/wazero";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (
        pkgs:
        let
          wasi-sdk = pkgs.callPackage ./nix/wasi-sdk.nix { };
          cLibs = import ./nix/c-libs.nix { inherit pkgs wasi-sdk; };
        in
        cLibs
        // {
          inherit wasi-sdk;
          zlib-wasi = cLibs.zlib;
          # wasm-ld as a WASI module, for linking interpreters from Go.
          lld-wasi = pkgs.callPackage ./nix/lld-wasi.nix { inherit wasi-sdk; };
          sysroot = pkgs.symlinkJoin {
            name = "pywasi-sysroot";
            paths = builtins.attrValues cLibs;
          };
        }
      );

      devShells = forAllSystems (
        pkgs:
        let
          pywasiPkgs = self.packages.${pkgs.stdenv.hostPlatform.system};
        in
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              python3
              uv

              # Toolchain for building CPython (and later C extensions) for WASI.
              pywasiPkgs.wasi-sdk
              wasm-tools
              go
              golangci-lint
              ninja
              pkg-config

              # WASI runtimes to test the output against.
              wasmtime
              wazero
            ];

            # Make uv use the Nix-provided interpreter instead of downloading its own.
            env = {
              WASI_SDK_PATH = "${pywasiPkgs.wasi-sdk}";
              # Static C libraries for wasm32-wasip1 (nix/c-libs.nix).
              PYWASI_ZLIB = "${pywasiPkgs.zlib-wasi}";
              PYWASI_SYSROOT = "${pywasiPkgs.sysroot}";
              UV_PYTHON = "${pkgs.python3}/bin/python3";
              UV_PYTHON_DOWNLOADS = "never";
            };
          };
        }
      );

      formatter = forAllSystems (pkgs: pkgs.nixfmt-tree);
    };
}
