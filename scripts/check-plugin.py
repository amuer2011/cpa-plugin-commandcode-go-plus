#!/usr/bin/env python3
"""Load a native release artifact and verify its ABI registration, without credentials/network."""
import ctypes
import json
from pathlib import Path
import struct
import sys


def check_plugin(path: Path, version: str, arch: str) -> None:
    expected_machine = {"amd64": 62, "arm64": 183}[arch]
    with path.open("rb") as binary:
        header = binary.read(20)
    if header[:6] != b"\x7fELF\x02\x01":
        raise RuntimeError("expected a little-endian 64-bit ELF shared library")
    elf_type, machine = struct.unpack_from("<HH", header, 16)
    if elf_type != 3 or machine != expected_machine:
        raise RuntimeError(f"wrong ELF type/architecture: {elf_type}/{machine}, expected {arch}")

    class Buffer(ctypes.Structure):
        _fields_ = [("ptr", ctypes.c_void_p), ("length", ctypes.c_size_t)]

    library = ctypes.CDLL(str(path.resolve()))
    # Confirm the host entrypoint exists as well as the exported call/free functions.
    getattr(library, "cliproxy_plugin_init")
    call = library.CommandCodeGoPluginCall
    call.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_size_t, ctypes.POINTER(Buffer)]
    call.restype = ctypes.c_int
    free = library.CommandCodeGoPluginFree
    free.argtypes = [ctypes.c_void_p, ctypes.c_size_t]
    free.restype = None
    shutdown = library.CommandCodeGoPluginShutdown
    shutdown.argtypes = []
    shutdown.restype = None

    def invoke(method: bytes) -> dict:
        response = Buffer()
        try:
            status = call(method, b"{}", 2, ctypes.byref(response))
            if not response.ptr or not response.length:
                raise RuntimeError(f"{method!r} returned no response")
            envelope = json.loads(ctypes.string_at(response.ptr, response.length))
            if status != 0 or not envelope.get("ok"):
                raise RuntimeError(f"{method!r} failed: {envelope}")
            return envelope["result"]
        finally:
            if response.ptr:
                free(response.ptr, response.length)

    try:
        registration = invoke(b"plugin.register")
        metadata = registration["metadata"]
        # The pinned Go SDK's Metadata fields have no JSON tags (capitalized keys).
        if metadata["Name"] != "CommandCode" or metadata["Version"] != version:
            raise RuntimeError("unexpected plugin name or embedded release version")
        if invoke(b"executor.identifier")["identifier"] != "commandcode-go":
            raise RuntimeError("unexpected provider identifier")
    finally:
        shutdown()
    print(f"[check] {path.name}: Linux/{arch}, loadable ABI, version {version}")


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit("usage: check-plugin.py <plugin.so> <version-without-v> <amd64|arm64>")
    check_plugin(Path(sys.argv[1]), sys.argv[2], sys.argv[3])
