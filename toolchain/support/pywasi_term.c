/* Terminals for wasm32-wasip1: the <termios.h> calls (termios/termios.h)
 * that wasi-libc leaves out, implemented by pywasi's host functions
 * (module "pywasi", see wasiterm/terminal.go), and CPython's termios module, built
 * from its own Modules/termios.c and registered here.
 *
 * Only the standard descriptors can be terminals, the ones the host gave
 * the guest. tcsetattr sends the modes the host can apply (canonical input,
 * echo, signals, output processing and a few more) and keeps the rest here,
 * so tcgetattr returns what was set.
 *
 * Linked by toolchain/pywasi_build.py. */
#include <errno.h>
#include <stdarg.h>
#include <stdint.h>
#include <string.h>

#include "termios.h"

/* The host module the functions come from: pywasi's, unless a program
 * that is not the interpreter builds this with its own, which the Go
 * package go.chrisrx.dev/pywasi/wasiterm gives it (wasiterm.Export). Such
 * a program also leaves out CPython's termios module, with
 * -DWASITERM_NO_PYTHON. */
#ifndef WASITERM_MODULE
#define WASITERM_MODULE "pywasi"
#endif

#define HOST(name, params) \
    __attribute__((import_module(WASITERM_MODULE), import_name("term_" #name))) int32_t host_##name params;

/* 0 if fd is a host terminal, else an errno (ENOTTY). */
HOST(get, (int32_t fd))
/* Applies modes in Linux's values. vmin_vtime is c_cc[VMIN] | c_cc[VTIME] << 8. */
HOST(set, (int32_t fd, int32_t when, int32_t iflag, int32_t oflag, int32_t lflag, int32_t vmin_vtime))
/* Writes rows and columns as two uint16. */
HOST(size, (int32_t fd, uint16_t *size))

#define NTERM 3

static struct {
    int set;
    struct termios t;
} terms[NTERM];

static int fail(int32_t err) {
    errno = err;
    return -1;
}

/* A terminal as a host's starts out: canonical input with echo. */
static void cooked(struct termios *t) {
    memset(t, 0, sizeof *t);
    t->c_iflag = BRKINT | ICRNL | IXON | IUTF8;
    t->c_oflag = OPOST | ONLCR;
    t->c_cflag = CS8 | CREAD | B38400;
    t->c_lflag = ISIG | ICANON | ECHO | ECHOE | ECHOK | ECHOCTL | ECHOKE | IEXTEN;
    t->c_cc[VINTR] = 0x03;
    t->c_cc[VQUIT] = 0x1c;
    t->c_cc[VERASE] = 0x7f;
    t->c_cc[VKILL] = 0x15;
    t->c_cc[VEOF] = 0x04;
    t->c_cc[VMIN] = 1;
    t->c_cc[VSTART] = 0x11;
    t->c_cc[VSTOP] = 0x13;
    t->c_cc[VSUSP] = 0x1a;
    t->c_cc[VREPRINT] = 0x12;
    t->c_cc[VDISCARD] = 0x0f;
    t->c_cc[VWERASE] = 0x17;
    t->c_cc[VLNEXT] = 0x16;
    t->__c_ispeed = t->__c_ospeed = B38400;
}

/* 0 if fd is a terminal, else an errno. */
static int32_t check(int fd) {
    if (fd < 0) return EBADF;
    return host_get(fd);
}

int tcgetattr(int fd, struct termios *t) {
    int32_t err = check(fd);
    if (err) return fail(err);
    if (fd < NTERM && terms[fd].set)
        *t = terms[fd].t;
    else
        cooked(t);
    return 0;
}

int tcsetattr(int fd, int when, const struct termios *t) {
    if (when != TCSANOW && when != TCSADRAIN && when != TCSAFLUSH) return fail(EINVAL);
    int32_t err = check(fd);
    if (err) return fail(err);
    err = host_set(fd, when, t->c_iflag, t->c_oflag, t->c_lflag, t->c_cc[VMIN] | t->c_cc[VTIME] << 8);
    if (err) return fail(err);
    if (fd < NTERM) {
        terms[fd].set = 1;
        terms[fd].t = *t;
    }
    return 0;
}

speed_t cfgetispeed(const struct termios *t) { return t->__c_ispeed; }
speed_t cfgetospeed(const struct termios *t) { return t->__c_ospeed; }

int cfsetispeed(struct termios *t, speed_t speed) {
    t->__c_ispeed = speed;
    return 0;
}

int cfsetospeed(struct termios *t, speed_t speed) {
    t->__c_ospeed = speed;
    return 0;
}

/* Breaks, draining and flow control mean nothing to the host's terminal:
 * output is written through as it comes. */
int tcsendbreak(int fd, int duration) {
    (void)duration;
    int32_t err = check(fd);
    return err ? fail(err) : 0;
}

int tcdrain(int fd) {
    int32_t err = check(fd);
    return err ? fail(err) : 0;
}

int tcflush(int fd, int queue) {
    if (queue != TCIFLUSH && queue != TCOFLUSH && queue != TCIOFLUSH) return fail(EINVAL);
    int32_t err = check(fd);
    return err ? fail(err) : 0;
}

int tcflow(int fd, int action) {
    if (action < TCOOFF || action > TCION) return fail(EINVAL);
    int32_t err = check(fd);
    return err ? fail(err) : 0;
}

#undef ioctl

int pywasi_term_ioctl(int fd, int request, ...) {
    va_list ap;
    va_start(ap, request);
    void *arg = va_arg(ap, void *);
    va_end(ap);
    if (request != TIOCGWINSZ) return ioctl(fd, request, arg);
    if (fd < 0) return fail(EBADF);
    uint16_t size[2];
    int32_t err = host_size(fd, size);
    if (err) return fail(err);
    struct winsize *w = arg;
    memset(w, 0, sizeof *w);
    w->ws_row = size[0];
    w->ws_col = size[1];
    return 0;
}

#ifndef WASITERM_NO_PYTHON
/* CPython's termios module, compiled with this file's header. */
extern void *PyInit_termios(void);
extern void pywasi_register(const char *name, void *(*init)(void));

__attribute__((constructor)) static void pywasi_register_termios(void) {
    pywasi_register("termios", PyInit_termios);
}
#endif
