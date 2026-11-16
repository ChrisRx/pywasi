{
  description = "pywasi: Python (with numpy) compiled to WASI modules for wasmtime/wazero";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # Rust with the wasm32-wasip1 standard library, for Rust extensions.
    rust-overlay = {
      url = "github:oxalica/rust-overlay";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      rust-overlay,
    }:
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
          # Pinned: Rust builds are cached by TOOLCHAIN_REV, not by compiler.
          rust = (rust-overlay.lib.mkRustBin { } pkgs).stable."1.100.0".minimal.override {
            targets = [ "wasm32-wasip1" ];
          };
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
              go-task # Taskfile.yml
              ninja
              pkg-config
              # Rust extensions (maturin), see toolchain/wasi-rust-ld.
              rust

              # WASI runtimes to test the output against.
              wasmtime
              wazero
            ];

            # Make uv use the Nix-provided interpreter instead of downloading its own.
            env = {
              WASI_SDK_PATH = "${pywasiPkgs.wasi-sdk}";
              # Static C libraries for wasm32-wasip1 (nix/c-libs.nix).
              PYWASI_ZLIB = "${pywasiPkgs.zlib-wasi}";
              PYWASI_OPENSSL = "${pywasiPkgs.openssl}";
              PYWASI_SQLITE = "${pywasiPkgs.sqlite}";
              PYWASI_BZIP2 = "${pywasiPkgs.bzip2}";
              PYWASI_XZ = "${pywasiPkgs.xz}";
              PYWASI_ZSTD = "${pywasiPkgs.zstd}";
              # CA certificates shipped with interpreters, for ssl.
              PYWASI_CACERT = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
              # Time zone database shipped with interpreters, for zoneinfo.
              PYWASI_ZONEINFO = "${pkgs.tzdata}/share/zoneinfo";
              PYWASI_SYSROOT = "${pywasiPkgs.sysroot}";
              # libstdc++ for native manylinux wheels that build tools import
              # (numpy for scipy's f2py), which NixOS has no system copy of.
              PYWASI_NATIVE_LIBS = "${pkgs.stdenv.cc.cc.lib}/lib";
              UV_PYTHON = "${pkgs.python3}/bin/python3";
              UV_PYTHON_DOWNLOADS = "never";
            };
          };
        }
      );

      formatter = forAllSystems (pkgs: pkgs.nixfmt-tree);
    };
}
