# OpenBLAS (BLAS + CBLAS + LAPACK) for wasm32-wasip1, static, single
# threaded, using its WASM128_GENERIC target (SIMD128 gemm kernels).
# LAPACK comes from OpenBLAS's C translation, so no Fortran compiler.
{
  stdenv,
  openblas,
  perl,
  writeShellScript,
  wasi-sdk,
}:
let
  # OpenBLAS derives a cross prefix from dirname($CC), so CC can't carry
  # arguments.
  cc = writeShellScript "wasm32-wasip1-clang" ''
    exec ${wasi-sdk}/bin/clang --target=wasm32-wasip1 --sysroot=${wasi-sdk}/share/wasi-sysroot "$@"
  '';
in
stdenv.mkDerivation {
  pname = "openblas-wasi";
  inherit (openblas) version src;

  # Keep flags with spaces (COMMON_OPT=...) intact.
  __structuredAttrs = true;

  nativeBuildInputs = [ perl ];

  postPatch = ''
    patchShebangs .

    # Only Emscripten is recognized as wasm. WASI needs no OS_* define.
    substituteInPlace ctest.c --replace-fail \
      '#if defined(__EMSCRIPTEN__)' \
      '#if defined(__wasi__)
    ARCH_WASM
    #endif

    #if defined(__EMSCRIPTEN__)'

    # sys/ipc.h is SysV IPC too, and WASI has none.
    substituteInPlace driver/others/memory.c --replace-fail \
      '#endif
    #include <sys/ipc.h>' \
      '#include <sys/ipc.h>
    #endif'

    # 16 MiB is below SGEMM's largest P/Q/R blocks (OpenBLAS warns that
    # large calculations may crash).
    substituteInPlace common_wasm.h --replace-fail \
      '( 16 << 20)' '( 32 << 20)'

    # Fortran ABI: give every Fortran-callable routine the f2c convention of
    # returning int. OpenBLAS's C-translated LAPACK and its BLAS interface
    # return void, while callers such as numpy and SciPy declare int. That's
    # harmless natively, but wasm checks call signatures, and wasm-ld turns
    # a mismatched call into a trap.
    find lapack-netlib -name '*.c' -not -path '*/LAPACKE/*' -exec sed -i -E \
      -e 's|/\* Subroutine \*/ void|/* Subroutine */ int|g' \
      -e 's/extern([[:space:]]+)void([[:space:]]+[a-z0-9_]+_[[:space:]]*\()/extern\1int\2/g' {} +
    sed -i -E \
      -e 's/^void([[:space:]]+)NAME([[:space:]]*\()/int\1NAME\2/' \
      -e 's/^void([[:space:]]+)NAME$/int\1NAME/' interface/*.c
    sed -i -E 's/\bvoid([[:space:]]+)BLASFUNC\(/int\1BLASFUNC(/' common_interface.h
    sed -i -E 's/^void (LAED4|LACPY|LASET)\(/int \1(/' lapack/laed3/laed3_*.c
    # A few translated routines declare and call xerbla_ without the hidden
    # string length (declarations may wrap lines).
    find lapack-netlib/SRC -name '*.c' -exec perl -0pi -e '
      s/xerbla_\(\s*char\s*\*,\s*integer\s*\*\s*\)/xerbla_(char *, integer *, ftnlen)/g;
      s/xerbla_\(("[A-Z0-9_]+"),\s*([^,()]+)\)/xerbla_($1, $2, (ftnlen)(sizeof $1 - 1))/g;
    ' {} +

    # A register-blocked 4x4 DGEMM kernel (patches/openblas-dgemm-wasm128-4x4.c)
    # in place of the generic 2x2 dot-product one, also used as the DTRMM
    # kernel, with the packing routines that match its unroll.
    cp ${./patches/openblas-dgemm-wasm128-4x4.c} kernel/wasm/dgemm_kernel_4x4_wasm128.c
    sed -i -E \
      -e 's|^DGEMMKERNEL( *)=.*|DGEMMKERNEL\1= dgemm_kernel_4x4_wasm128.c|' \
      -e 's|^DGEMMONCOPY( *)=.*|DGEMMONCOPY\1= ../generic/gemm_ncopy_4.c|' \
      -e 's|^DGEMMOTCOPY( *)=.*|DGEMMOTCOPY\1= ../generic/gemm_tcopy_4.c|' \
      -e 's|^DTRMMKERNEL(\s*)=.*|DTRMMKERNEL\1= dgemm_kernel_4x4_wasm128.c|' \
      kernel/wasm/KERNEL.WASM128_GENERIC
    # The base KERNEL file sets the TRMM kernel unconditionally (it is read
    # after the target's), so it needs the change too.
    sed -i -E 's|^DTRMMKERNEL(\s*)=.*|DTRMMKERNEL\1= dgemm_kernel_4x4_wasm128.c|' kernel/wasm/KERNEL
    sed -i -E '/defined\(RISCV64_GENERIC\) \|\| defined\(WASM128_GENERIC\)/,/DGEMM_DEFAULT_UNROLL_N/ s/(DGEMM_DEFAULT_UNROLL_[MN]) +2/\1  4/' param.h
    grep -q 'DGEMMKERNEL *= dgemm_kernel_4x4_wasm128.c' kernel/wasm/KERNEL.WASM128_GENERIC
    grep -q 'DTRMMKERNEL.*dgemm_kernel_4x4_wasm128.c' kernel/wasm/KERNEL.WASM128_GENERIC
    grep -q 'DTRMMKERNEL.*dgemm_kernel_4x4_wasm128.c' kernel/wasm/KERNEL
    [ "$(grep -c 'DGEMM_DEFAULT_UNROLL_[MN]  4' param.h)" -ge 2 ]
  '';

  makeFlags = [
    "CC=${cc}"
    "AR=${wasi-sdk}/bin/llvm-ar"
    "RANLIB=${wasi-sdk}/bin/llvm-ranlib"
    # getarch runs on the build machine.
    "HOSTCC=${stdenv.cc}/bin/cc"
    "TARGET=WASM128_GENERIC"
    "BINARY=32"
    "CROSS=1"
    "DYNAMIC_ARCH=0"
    "USE_THREAD=0"
    "USE_LOCKING=0"
    "NUM_THREADS=1"
    "NOFORTRAN=1"
    "C_LAPACK=1"
    "NO_LAPACKE=1"
    "NO_SHARED=1"
    # -Wno-return-mismatch: bare `return;` in routines now returning int.
    "COMMON_OPT=-O2 -msimd128 -DNO_SYSV_IPC -D_WASI_EMULATED_MMAN -D_WASI_EMULATED_SIGNAL -D_WASI_EMULATED_PROCESS_CLOCKS -Wno-return-mismatch -Wno-return-type"
    "PREFIX=${placeholder "out"}"
  ];

  # libs is BLAS/CBLAS, netlib is LAPACK.
  buildFlags = [
    "libs"
    "netlib"
  ];

  # mmap etc. come from wasi-libc's emulation libraries. List them in the
  # .pc file so build systems' link checks (numpy's meson) succeed.
  postInstall = ''
    substituteInPlace $out/lib/pkgconfig/openblas.pc \
      --replace-fail 'extralib=' 'extralib=-lwasi-emulated-mman -lwasi-emulated-signal -lwasi-emulated-process-clocks' \
      --replace-fail 'Libs: -L''${libdir} -l''${libprefix}openblas''${libsuffix}''${libnamesuffix}' 'Libs: -L''${libdir} -l''${libprefix}openblas''${libsuffix}''${libnamesuffix} ''${extralib}'
  '';

  dontFixup = true;
}
