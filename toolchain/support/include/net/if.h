/* Network interfaces for wasm32-wasip1, which has none to list: the names
 * C code (httplib, say) refers to, implemented in pywasi_socket.c. */
#ifndef PYWASI_NET_IF_H
#define PYWASI_NET_IF_H

#define IFNAMSIZ 16
#define IF_NAMESIZE 16

struct if_nameindex {
    unsigned int if_index;
    char *if_name;
};

unsigned int if_nametoindex(const char *);
char *if_indextoname(unsigned int, char *);
struct if_nameindex *if_nameindex(void);
void if_freenameindex(struct if_nameindex *);

#endif
