/*
 * Register-blocked DGEMM micro-kernel for WebAssembly SIMD128 (pywasi).
 *
 * C += alpha * A * B for packed panels, with GEMM_UNROLL_M = GEMM_UNROLL_N
 * = 4 (gemm_ncopy_4 / gemm_tcopy_4 packing): A in strips of 4 rows (then
 * 2, then 1), k-major, and B in strips of 4 columns (then 2, then 1),
 * k-major.
 *
 * Each step of k loads 4 doubles of A as two f64x2 vectors, broadcasts 4
 * doubles of B, and updates 8 accumulators that hold the 4x4 block of C:
 * 16 multiply-adds for 6 loads. OpenBLAS's generic wasm128 kernel computes
 * 2x2 dot products instead, 8 multiply-adds for 4 loads plus lane
 * reductions, which leaves it bound by loads.
 *
 * Built with TRMMKERNEL (and LEFT/TRANSA) it is also the DTRMM kernel:
 * each block then covers only part of k, by the same offset rules as
 * kernel/generic/trmmkernel_2x2.c, and C is overwritten instead of added to.
 */
#include "common.h"
#include <wasm_simd128.h>

#define INLINE static inline __attribute__((always_inline))

#ifdef TRMMKERNEL
#define UPDATE(c, v) (v)
#else
#define UPDATE(c, v) wasm_f64x2_add((c), (v))
#endif

#ifdef TRMMKERNEL
/* Whether a block starts at k = off (and runs to bk) or at 0 (and runs to
 * off plus the block size). */
#if (defined(LEFT) && !defined(TRANSA)) || (!defined(LEFT) && defined(TRANSA))
#define TRMM_SKIP 1
#else
#define TRMM_SKIP 0
#endif
#endif

/* A block of MR (4 or 2) rows by NR (4, 2 or 1) columns. With constant
 * arguments the loops unroll and the accumulators stay in registers. */
INLINE void block_vec(const int MR, const int NR, BLASLONG bk, FLOAT alpha, const FLOAT *a, const FLOAT *b,
                      FLOAT *C, BLASLONG ldc) {
	v128_t acc[2][4];
	for (int j = 0; j < NR; j++) {
		acc[0][j] = wasm_f64x2_splat(0.0);
		acc[1][j] = wasm_f64x2_splat(0.0);
	}
	for (BLASLONG k = 0; k < bk; k++) {
		v128_t a0 = wasm_v128_load(a);
		v128_t a1 = MR == 4 ? wasm_v128_load(a + 2) : a0;
		for (int j = 0; j < NR; j++) {
			v128_t bj = wasm_v128_load64_splat(b + j);
			acc[0][j] = wasm_f64x2_add(acc[0][j], wasm_f64x2_mul(a0, bj));
			if (MR == 4) {
				acc[1][j] = wasm_f64x2_add(acc[1][j], wasm_f64x2_mul(a1, bj));
			}
		}
		a += MR;
		b += NR;
	}
	v128_t va = wasm_f64x2_splat(alpha);
	for (int j = 0; j < NR; j++) {
		FLOAT *c = C + j * ldc;
		wasm_v128_store(c, UPDATE(wasm_v128_load(c), wasm_f64x2_mul(va, acc[0][j])));
		if (MR == 4) {
			wasm_v128_store(c + 2, UPDATE(wasm_v128_load(c + 2), wasm_f64x2_mul(va, acc[1][j])));
		}
	}
}

/* A single row by NR columns. */
INLINE void block_row(const int NR, BLASLONG bk, FLOAT alpha, const FLOAT *a, const FLOAT *b, FLOAT *C,
                      BLASLONG ldc) {
	FLOAT acc[4] = {0, 0, 0, 0};
	for (BLASLONG k = 0; k < bk; k++) {
		for (int j = 0; j < NR; j++) {
			acc[j] += a[k] * b[k * NR + j];
		}
	}
	for (int j = 0; j < NR; j++) {
#ifdef TRMMKERNEL
		C[j * ldc] = alpha * acc[j];
#else
		C[j * ldc] += alpha * acc[j];
#endif
	}
}

/* One MR x NR block. For TRMM, only the part of k the offset allows. */
#ifdef TRMMKERNEL
#define BLOCK(MR, NR, CALL)                                                                       \
	do {                                                                                      \
		BLASLONG start = TRMM_SKIP ? *off : 0;                                            \
		BLASLONG count = TRMM_SKIP ? bk - *off : *off + (LEFT_SIDE ? (MR) : (NR));        \
		CALL(count, ba + start * (MR), bb + start * (NR));                                \
		if (LEFT_SIDE) {                                                                  \
			*off += (MR);                                                             \
		}                                                                                 \
	} while (0)
#ifdef LEFT
#define LEFT_SIDE 1
#else
#define LEFT_SIDE 0
#endif
#else
#define BLOCK(MR, NR, CALL) CALL(bk, ba, bb)
#endif

/* All row blocks of one column strip of width NR. */
INLINE void strip(const int NR, BLASLONG bm, BLASLONG bk, FLOAT alpha, const FLOAT *ba, const FLOAT *bb,
                  FLOAT *C, BLASLONG ldc, BLASLONG *off) {
	(void)off;
	BLASLONG i = 0;
#define VEC4(K, A, B) block_vec(4, NR, (K), alpha, (A), (B), C + i, ldc)
#define VEC2(K, A, B) block_vec(2, NR, (K), alpha, (A), (B), C + i, ldc)
#define ROW1(K, A, B) block_row(NR, (K), alpha, (A), (B), C + i, ldc)
	for (; i + 4 <= bm; i += 4) {
		BLOCK(4, NR, VEC4);
		ba += 4 * bk;
	}
	if (bm - i >= 2) {
		BLOCK(2, NR, VEC2);
		ba += 2 * bk;
		i += 2;
	}
	if (bm - i == 1) {
		BLOCK(1, NR, ROW1);
	}
#undef VEC4
#undef VEC2
#undef ROW1
}

int CNAME(BLASLONG bm, BLASLONG bn, BLASLONG bk, FLOAT alpha, FLOAT *ba, FLOAT *bb, FLOAT *C, BLASLONG ldc
#ifdef TRMMKERNEL
          ,
          BLASLONG offset
#endif
) {
	BLASLONG off = 0;
#if defined(TRMMKERNEL) && !defined(LEFT)
	off = -offset;
#endif
	BLASLONG j = 0;
#if defined(TRMMKERNEL) && defined(LEFT)
#define STRIP_START off = offset
#else
#define STRIP_START (void)0
#endif
#if defined(TRMMKERNEL) && !defined(LEFT)
#define STRIP_END(NR) off += (NR)
#else
#define STRIP_END(NR) (void)0
#endif
	for (; j + 4 <= bn; j += 4) {
		STRIP_START;
		strip(4, bm, bk, alpha, ba, bb, C + j * ldc, ldc, &off);
		STRIP_END(4);
		bb += 4 * bk;
	}
	if (bn - j >= 2) {
		STRIP_START;
		strip(2, bm, bk, alpha, ba, bb, C + j * ldc, ldc, &off);
		STRIP_END(2);
		bb += 2 * bk;
		j += 2;
	}
	if (bn - j == 1) {
		STRIP_START;
		strip(1, bm, bk, alpha, ba, bb, C + j * ldc, ldc, &off);
		STRIP_END(1);
	}
	return 0;
}
