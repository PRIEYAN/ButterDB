#ifndef BUTTERDB_NATIVEPLAYER_X11_H
#define BUTTERDB_NATIVEPLAYER_X11_H

#include <X11/Xlib.h>

typedef struct {
	Display *display;
	Window parent;
	Window child;
} bdb_x11_view;

bdb_x11_view* bdb_x11_create(unsigned long parent_id, double x, double y, double w, double h);
unsigned long bdb_x11_child(bdb_x11_view *view);
void bdb_x11_resize(bdb_x11_view *view, double x, double y, double w, double h);
void bdb_x11_destroy(bdb_x11_view *view);

#endif
