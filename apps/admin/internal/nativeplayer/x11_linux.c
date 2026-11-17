#include "x11_linux.h"

#include <pthread.h>
#include <X11/Xatom.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>



#define BDB_MAX_DISPLAYS 64

static pthread_mutex_t bdb_displays_mu = PTHREAD_MUTEX_INITIALIZER;
static Display *bdb_displays[BDB_MAX_DISPLAYS];
static XErrorHandler bdb_previous_handler = NULL;
static pthread_once_t bdb_handler_once = PTHREAD_ONCE_INIT;

static int bdb_owns_display(Display *display) {
	int owned = 0;
	pthread_mutex_lock(&bdb_displays_mu);
	for (int i = 0; i < BDB_MAX_DISPLAYS; i++) {
		if (bdb_displays[i] == display) {
			owned = 1;
			break;
		}
	}
	pthread_mutex_unlock(&bdb_displays_mu);
	return owned;
}

static int bdb_x11_error_handler(Display *display, XErrorEvent *event) {
	if (bdb_owns_display(display)) {
		return 0;
	}
	if (bdb_previous_handler != NULL) {
		return bdb_previous_handler(display, event);
	}
	return 0;
}

static void bdb_install_error_handler(void) {
	bdb_previous_handler = XSetErrorHandler(bdb_x11_error_handler);
}

static void bdb_track_display(Display *display, int add) {
	pthread_once(&bdb_handler_once, bdb_install_error_handler);
	pthread_mutex_lock(&bdb_displays_mu);
	for (int i = 0; i < BDB_MAX_DISPLAYS; i++) {
		if (add && bdb_displays[i] == NULL) {
			bdb_displays[i] = display;
			break;
		}
		if (!add && bdb_displays[i] == display) {
			bdb_displays[i] = NULL;
			break;
		}
	}
	pthread_mutex_unlock(&bdb_displays_mu);
}

__attribute__((constructor))
static void bdb_x11_init_threads(void) {
	XInitThreads();
}

static int bdb_x11_window_viewable(Display *display, Window window) {
	XWindowAttributes attrs;
	if (XGetWindowAttributes(display, window, &attrs) == 0) {
		return 0;
	}
	return attrs.map_state == IsViewable;
}

static int bdb_x11_window_pid(Display *display, Window window) {
	Atom pid_atom = XInternAtom(display, "_NET_WM_PID", True);
	if (pid_atom == None) {
		return 0;
	}

	Atom actual_type;
	int actual_format;
	unsigned long nitems;
	unsigned long bytes_after;
	unsigned char *data = NULL;
	int status = XGetWindowProperty(
		display,
		window,
		pid_atom,
		0,
		1,
		False,
		XA_CARDINAL,
		&actual_type,
		&actual_format,
		&nitems,
		&bytes_after,
		&data
	);
	if (status != Success || data == NULL || nitems == 0) {
		if (data != NULL) {
			XFree(data);
		}
		return 0;
	}
	long pid = ((long *)data)[0];
	XFree(data);
	return (int)pid;
}

static int bdb_bytes_contains(const unsigned char *value, unsigned long value_length, const char *needle) {
	if (value == NULL || needle == NULL) {
		return 0;
	}
	size_t needle_length = strlen(needle);
	if (needle_length == 0 || value_length < needle_length) {
		return 0;
	}
	for (unsigned long index = 0; index <= value_length - needle_length; index++) {
		if (memcmp(value + index, needle, needle_length) == 0) {
			return 1;
		}
	}
	return 0;
}

static int bdb_x11_title_contains(Display *display, Window window, const char *needle) {
	if (needle == NULL || needle[0] == '\0') {
		return 0;
	}

	Atom utf8 = XInternAtom(display, "UTF8_STRING", True);
	Atom net_name = XInternAtom(display, "_NET_WM_NAME", True);
	if (utf8 != None && net_name != None) {
		Atom actual_type;
		int actual_format;
		unsigned long nitems;
		unsigned long bytes_after;
		unsigned char *data = NULL;
		int status = XGetWindowProperty(
			display,
			window,
			net_name,
			0,
			1024,
			False,
			utf8,
			&actual_type,
			&actual_format,
			&nitems,
			&bytes_after,
			&data
		);
		if (status == Success && data != NULL) {
			int contains = actual_format == 8 && bdb_bytes_contains(data, nitems, needle);
			XFree(data);
			if (contains) {
				return 1;
			}
		}
	}

	char *wm_name = NULL;
	if (XFetchName(display, window, &wm_name) != 0 && wm_name != NULL) {
		int contains = strstr(wm_name, needle) != NULL;
		XFree(wm_name);
		return contains;
	}
	return 0;
}

static Window bdb_x11_find_window(Display *display) {
	Window root = DefaultRootWindow(display);
	Atom client_list_atom = XInternAtom(display, "_NET_CLIENT_LIST", True);
	Window preferred = 0;
	Window first = 0;
	Window *windows = NULL;
	unsigned long count = 0;

	if (client_list_atom != None) {
		Atom actual_type;
		int actual_format;
		unsigned long bytes_after;
		unsigned char *data = NULL;
		int status = XGetWindowProperty(
			display,
			root,
			client_list_atom,
			0,
			4096,
			False,
			XA_WINDOW,
			&actual_type,
			&actual_format,
			&count,
			&bytes_after,
			&data
		);
		if (status == Success && data != NULL && count > 0) {
			windows = (Window *)data;
		}
	}

	if (windows == NULL) {
		Window root_return;
		Window parent_return;
		Window *children = NULL;
		unsigned int child_count = 0;
		if (XQueryTree(display, root, &root_return, &parent_return, &children, &child_count) == 0) {
			return 0;
		}
		windows = children;
		count = child_count;
	}

	int pid = (int)getpid();
	for (unsigned long i = 0; i < count; i++) {
		Window candidate = windows[i];
		if (!bdb_x11_window_viewable(display, candidate)) {
			continue;
		}
		if (bdb_x11_window_pid(display, candidate) != pid) {
			continue;
		}
		if (first == 0) {
			first = candidate;
		}
		if (bdb_x11_title_contains(display, candidate, "butterDB")) {
			preferred = candidate;
			break;
		}
	}

	if (windows != NULL) {
		XFree(windows);
	}
	return preferred != 0 ? preferred : first;
}

static int bdb_positive(double value) {
	if (value < 1) {
		return 1;
	}
	return (int)(value + 0.5);
}

static int bdb_nonnegative(double value) {
	if (value < 0) {
		return 0;
	}
	return (int)(value + 0.5);
}

bdb_x11_view* bdb_x11_create(unsigned long parent_id, double x, double y, double w, double h) {
	Display *display = XOpenDisplay(NULL);
	if (display == NULL) {
		return NULL;
	}
	bdb_track_display(display, 1);

	Window parent = (Window)parent_id;
	if (parent == 0) {
		parent = bdb_x11_find_window(display);
	}
	if (parent == 0) {
		XCloseDisplay(display);
		bdb_track_display(display, 0);
		return NULL;
	}

	int screen = DefaultScreen(display);
	Window child = XCreateSimpleWindow(
		display,
		parent,
		bdb_nonnegative(x),
		bdb_nonnegative(y),
		bdb_positive(w),
		bdb_positive(h),
		0,
		BlackPixel(display, screen),
		BlackPixel(display, screen)
	);
	if (child == 0) {
		XCloseDisplay(display);
		bdb_track_display(display, 0);
		return NULL;
	}

	XMapRaised(display, child);
	XFlush(display);

	bdb_x11_view *view = (bdb_x11_view *)calloc(1, sizeof(bdb_x11_view));
	if (view == NULL) {
		XDestroyWindow(display, child);
		XCloseDisplay(display);
		bdb_track_display(display, 0);
		return NULL;
	}
	view->display = display;
	view->parent = parent;
	view->child = child;
	return view;
}

unsigned long bdb_x11_child(bdb_x11_view *view) {
	if (view == NULL) {
		return 0;
	}
	return (unsigned long)view->child;
}

void bdb_x11_resize(bdb_x11_view *view, double x, double y, double w, double h) {
	if (view == NULL || view->display == NULL || view->child == 0) {
		return;
	}
	XMoveResizeWindow(
		view->display,
		view->child,
		bdb_nonnegative(x),
		bdb_nonnegative(y),
		bdb_positive(w),
		bdb_positive(h)
	);
	XFlush(view->display);
}

void bdb_x11_destroy(bdb_x11_view *view) {
	if (view == NULL) {
		return;
	}
	if (view->display != NULL) {
		if (view->child != 0) {
			XUnmapWindow(view->display, view->child);
			XDestroyWindow(view->display, view->child);
			view->child = 0;
		}
		XSync(view->display, True);
		XCloseDisplay(view->display);
		bdb_track_display(view->display, 0);
		view->display = NULL;
	}
	free(view);
}
