import ctypes
import os
from pathlib import Path
import sys
import time

library, address, key = sys.argv[1:]
directory = Path(library).resolve().parent
dll_directory = os.add_dll_directory(str(directory))
mpv = ctypes.CDLL(str(Path(library).resolve()))
mpv.mpv_create.restype = ctypes.c_void_p
mpv.mpv_initialize.argtypes = [ctypes.c_void_p]
mpv.mpv_set_option_string.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_char_p]
mpv.mpv_set_property_string.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_char_p]
mpv.mpv_command.argtypes = [ctypes.c_void_p, ctypes.POINTER(ctypes.c_char_p)]
mpv.mpv_terminate_destroy.argtypes = [ctypes.c_void_p]

class Event(ctypes.Structure):
    _fields_ = [('event_id', ctypes.c_int), ('error', ctypes.c_int),
                ('reply_userdata', ctypes.c_uint64), ('data', ctypes.c_void_p)]

class EndFile(ctypes.Structure):
    _fields_ = [('reason', ctypes.c_int), ('error', ctypes.c_int)]

mpv.mpv_wait_event.argtypes = [ctypes.c_void_p, ctypes.c_double]
mpv.mpv_wait_event.restype = ctypes.POINTER(Event)
handle = mpv.mpv_create()
if not handle:
    raise RuntimeError('mpv_create failed')

def check(result, operation):
    if result < 0:
        raise RuntimeError(f'{operation}: libmpv error {result}')

try:
    for name, value in [('config', 'no'), ('vo', 'null'), ('ao', 'null'),
                        ('hwdec', 'no'), ('terminal', 'yes')]:
        check(mpv.mpv_set_option_string(handle, name.encode(), value.encode()), name)
    check(mpv.mpv_initialize(handle), 'initialize')
    options = 'seg_max_retry=3,strict=experimental,allowed_extensions=ALL,protocol_whitelist=[http,https,tcp,tls,crypto,data,file]'
    if key:
        options += ',decryption_key=' + key
    check(mpv.mpv_set_property_string(handle, b'demuxer-lavf-o', options.encode()), 'demuxer-lavf-o')
    command = (ctypes.c_char_p * 3)(b'loadfile', address.encode(), None)
    check(mpv.mpv_command(handle, command), 'loadfile')
    loaded = False
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        event = mpv.mpv_wait_event(handle, 0.5).contents
        if event.event_id == 8:
            loaded = True
        if event.event_id == 7:
            end = ctypes.cast(event.data, ctypes.POINTER(EndFile)).contents
            if not loaded or end.reason != 0 or end.error < 0:
                raise RuntimeError(f'playback failed: loaded={loaded}, reason={end.reason}, error={end.error}')
            print('Packaged Windows libmpv decoded online media through the Go stream server to EOF.')
            break
    else:
        raise RuntimeError('playback timed out')
finally:
    mpv.mpv_terminate_destroy(handle)
    dll_directory.close()
