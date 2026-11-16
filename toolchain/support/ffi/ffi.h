/* The part of libffi's API that CPython's _ctypes uses, for wasm32-wasip1,
 * implemented by toolchain/support/pywasi_ffi.c. libffi has no WASI port:
 * types are described as libffi does, so ctypes structures, pointers and
 * arrays work, but calls are limited (see pywasi_ffi.c) and callbacks
 * (closures) aren't supported.
 */
#ifndef PYWASI_FFI_H
#define PYWASI_FFI_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

#define FFI_TYPE_VOID 0
#define FFI_TYPE_INT 1
#define FFI_TYPE_FLOAT 2
#define FFI_TYPE_DOUBLE 3
#define FFI_TYPE_LONGDOUBLE 4
#define FFI_TYPE_UINT8 5
#define FFI_TYPE_SINT8 6
#define FFI_TYPE_UINT16 7
#define FFI_TYPE_SINT16 8
#define FFI_TYPE_UINT32 9
#define FFI_TYPE_SINT32 10
#define FFI_TYPE_UINT64 11
#define FFI_TYPE_SINT64 12
#define FFI_TYPE_STRUCT 13
#define FFI_TYPE_POINTER 14
#define FFI_TYPE_COMPLEX 15

#define FFI_CLOSURES 1

typedef enum ffi_abi { FFI_FIRST_ABI = 0, FFI_DEFAULT_ABI = 1, FFI_LAST_ABI } ffi_abi;

typedef enum ffi_status { FFI_OK = 0, FFI_BAD_TYPEDEF, FFI_BAD_ABI, FFI_BAD_ARGTYPE } ffi_status;

typedef unsigned long ffi_arg;
typedef signed long ffi_sarg;

typedef struct _ffi_type {
    size_t size;
    unsigned short alignment;
    unsigned short type;
    struct _ffi_type **elements;
} ffi_type;

typedef struct {
    ffi_abi abi;
    unsigned nargs;
    ffi_type **arg_types;
    ffi_type *rtype;
    unsigned bytes;
    unsigned flags;
} ffi_cif;

typedef struct {
    ffi_cif *cif;
    void (*fun)(ffi_cif *, void *, void **, void *);
    void *user_data;
} ffi_closure;

extern ffi_type ffi_type_void;
extern ffi_type ffi_type_uint8;
extern ffi_type ffi_type_sint8;
extern ffi_type ffi_type_uint16;
extern ffi_type ffi_type_sint16;
extern ffi_type ffi_type_uint32;
extern ffi_type ffi_type_sint32;
extern ffi_type ffi_type_uint64;
extern ffi_type ffi_type_sint64;
extern ffi_type ffi_type_float;
extern ffi_type ffi_type_double;
extern ffi_type ffi_type_longdouble;
extern ffi_type ffi_type_pointer;

/* wasm32: int and long are 32 bits. */
#define ffi_type_uchar ffi_type_uint8
#define ffi_type_schar ffi_type_sint8
#define ffi_type_ushort ffi_type_uint16
#define ffi_type_sshort ffi_type_sint16
#define ffi_type_uint ffi_type_uint32
#define ffi_type_sint ffi_type_sint32
#define ffi_type_ulong ffi_type_uint32
#define ffi_type_slong ffi_type_sint32

#define FFI_FN(f) ((void (*)(void))(f))

ffi_status ffi_prep_cif(ffi_cif *cif, ffi_abi abi, unsigned int nargs, ffi_type *rtype, ffi_type **atypes);
ffi_status ffi_prep_cif_var(ffi_cif *cif, ffi_abi abi, unsigned int nfixedargs, unsigned int ntotalargs,
                            ffi_type *rtype, ffi_type **atypes);
void ffi_call(ffi_cif *cif, void (*fn)(void), void *rvalue, void **avalue);

void *ffi_closure_alloc(size_t size, void **code);
void ffi_closure_free(void *closure);
ffi_status ffi_prep_closure_loc(ffi_closure *closure, ffi_cif *cif,
                                void (*fun)(ffi_cif *, void *, void **, void *), void *user_data, void *codeloc);
/* Deprecated, still compiled (not called) by _ctypes. */
ffi_status ffi_prep_closure(ffi_closure *closure, ffi_cif *cif,
                            void (*fun)(ffi_cif *, void *, void **, void *), void *user_data);

#ifdef __cplusplus
}
#endif

#endif
