# Static OpenSSL (libssl, libcrypto) for wasm32-wasip1, for CPython's ssl
# and hashlib. Sockets and name resolution come from
# toolchain/support/pywasi_socket.c (its headers are used here), so it
# links into pywasi interpreters only. Certificates are read from
# /etc/ssl/cert.pem in the guest (OPENSSLDIR).
{
  stdenvNoCC,
  openssl,
  perl,
  wasi-sdk,
}:
stdenvNoCC.mkDerivation {
  pname = "openssl-wasi";
  inherit (openssl) version src;

  nativeBuildInputs = [ perl ];

  env = {
    CC = "${wasi-sdk}/bin/clang --target=wasm32-wasip1 --sysroot=${wasi-sdk}/share/wasi-sysroot";
    AR = "${wasi-sdk}/bin/llvm-ar";
    RANLIB = "${wasi-sdk}/bin/llvm-ranlib";
  };

  # Threads are wasi-libc's single-threaded pthread stubs (CPython wants
  # OPENSSL_THREADS). no-dso etc.: WASI preview 1 has no dlopen or async
  # (ucontext). no-dgram and no-quic: only TCP sockets exist. The random
  # generator is seeded with getentropy (WASI random_get).
  configurePhase = ''
    runHook preConfigure
    patchShebangs Configure
    ./Configure linux-generic32 \
      --prefix=$out --libdir=lib --openssldir=/etc/ssl \
      no-asm no-shared no-dso no-engine no-afalgeng no-async \
      no-ui-console no-tests no-apps no-docs no-dgram no-quic no-ktls \
      no-secure-memory no-thread-pool no-default-thread-pool \
      --with-rand-seed=getrandom \
      -O2 -msimd128 -I${../toolchain/support/include} \
      -D_WASI_EMULATED_SIGNAL -D_WASI_EMULATED_PROCESS_CLOCKS \
      -D_WASI_EMULATED_MMAN -D_WASI_EMULATED_GETPID \
      -DOPENSSL_NO_UNIX_SOCK
    # -pthread makes clang emit wasm atomics (the threads proposal), which
    # wazero doesn't support. The stubs need neither.
    sed -i 's/-pthread//g' Makefile
    runHook postConfigure
  '';

  buildFlags = [ "build_libs" ];
  installTargets = [ "install_dev" ];
  enableParallelBuilding = true;

  dontFixup = true;
}
