/*
 * Software floating-point status word for numpy on wasm
 * (toolchain/patches/numpy-wasm-fpstatus.patch). Defined once here so every
 * extension module's private copy of npymath shares it. Linked into every
 * python.wasm, and unused without numpy.
 */
int __pywasi_npy_fpstatus = 0;
