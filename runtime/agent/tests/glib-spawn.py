"""Run inside the agent image with the production seccomp profile applied."""

import ctypes
import ctypes.util


class GError(ctypes.Structure):
    _fields_ = [
        ("domain", ctypes.c_uint),
        ("code", ctypes.c_int),
        ("message", ctypes.c_char_p),
    ]


glib = ctypes.CDLL(ctypes.util.find_library("glib-2.0"))
glib.g_spawn_command_line_sync.argtypes = [
    ctypes.c_char_p,
    ctypes.c_void_p,
    ctypes.c_void_p,
    ctypes.POINTER(ctypes.c_int),
    ctypes.POINTER(ctypes.POINTER(GError)),
]
glib.g_spawn_command_line_sync.restype = ctypes.c_int
status = ctypes.c_int()
error = ctypes.POINTER(GError)()
if not glib.g_spawn_command_line_sync(
    b"/bin/true", None, None, ctypes.byref(status), ctypes.byref(error)
):
    raise SystemExit(error.contents.message.decode() if error else "GLib spawn failed")
if status.value != 0:
    raise SystemExit(f"Child process failed: {status.value}")
print("GLib child process launch passed")
