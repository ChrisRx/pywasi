# Static SQLite for wasm32-wasip1, for CPython's sqlite3 module. Built from
# the amalgamation, which needs no configure step. SQLite detects WASI and
# leaves out threads, extension loading, file locking and WAL mode.
{
  stdenvNoCC,
  fetchurl,
  unzip,
  wasi-sdk,
}:
stdenvNoCC.mkDerivation rec {
  pname = "sqlite-wasi";
  version = "3.54.0";

  src = fetchurl {
    url = "https://sqlite.org/2026/sqlite-amalgamation-3540000.zip";
    hash = "sha256-aOkTsP6OxrTl85HFlKKIcohOgNbNBCoeUMlXvka1qk0=";
  };
  nativeBuildInputs = [ unzip ];

  buildPhase = ''
    runHook preBuild
    ${wasi-sdk}/bin/clang --target=wasm32-wasip1 --sysroot=${wasi-sdk}/share/wasi-sysroot \
      -O2 -D_WASI_EMULATED_GETPID -DSQLITE_OMIT_DEPRECATED -DSQLITE_DQS=0 \
      -DSQLITE_ENABLE_FTS5 -DSQLITE_ENABLE_MATH_FUNCTIONS -DSQLITE_ENABLE_JSON1 \
      -c sqlite3.c -o sqlite3.o
    ${wasi-sdk}/bin/llvm-ar rcs libsqlite3.a sqlite3.o
    runHook postBuild
  '';

  installPhase = ''
    runHook preInstall
    install -Dm644 libsqlite3.a $out/lib/libsqlite3.a
    install -Dm644 -t $out/include sqlite3.h sqlite3ext.h
    runHook postInstall
  '';

  dontFixup = true;
}
