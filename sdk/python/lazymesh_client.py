#!/usr/bin/env python3
"""Python client for lazymesh's unix-socket control plane.

Stdlib-only (socket + json), mirroring the Go sdk package: dial one
session's control socket, drive it headlessly, read turns as structured
values.

Usage:
    lazymesh_client.py <socket> session
    lazymesh_client.py <socket> say <text>       # blocks until turn_complete
    lazymesh_client.py <socket> query <what>     # status|rooms|inbox|agents|realms
    lazymesh_client.py <socket> interrupt
    lazymesh_client.py <socket> shutdown

`say` prints one JSON object: the session info plus the turn (deltas,
text, tool_calls, tool_results, errors, backed_off, max_failures).
`query` prints {"type": "query_result", "what": ..., "data": ...}.
Exit code 1 on any wire or protocol error.
"""

import json
import socket
import sys

ERR_EXIT = 1


def fail(message):
    print(json.dumps({"type": "error", "error": message}))
    sys.exit(ERR_EXIT)


def read_line(reader):
    line = reader.readline()
    if not line:
        fail("connection closed")
    return json.loads(line)


def send(sock, message):
    sock.sendall((json.dumps(message) + "\n").encode())


def session_cmd(reader, path):
    info = read_line(reader)
    info["socket"] = path
    print(json.dumps(info))


def say_cmd(sock, reader, info, text):
    send(sock, {"type": "input", "session_id": info["session_id"], "text": text})
    turn = {
        "deltas": [],
        "text": "",
        "tool_calls": [],
        "tool_results": [],
        "errors": [],
        "backed_off": False,
        "max_failures": False,
    }
    while True:
        event = read_line(reader)
        kind = event.get("type")
        if kind == "delta":
            turn["deltas"].append(event.get("text", ""))
        elif kind == "assistant":
            turn["text"] = event.get("text", "")
        elif kind == "tool_call":
            turn["tool_calls"].append({"tool": event.get("tool", ""), "args": event.get("args", "")})
        elif kind == "tool_result":
            turn["tool_results"].append({"tool": event.get("tool", ""), "result": event.get("result", "")})
        elif kind == "error":
            turn["errors"].append(event.get("error", ""))
        elif kind == "backoff":
            turn["backed_off"] = True
        elif kind == "max_failures":
            turn["max_failures"] = True
        elif kind == "turn_complete":
            break
    print(json.dumps({"session": info, "turn": turn}))


def main():
    if len(sys.argv) < 3:
        fail("usage: lazymesh_client.py <socket> <session|say|query|interrupt|shutdown> [arg]")
    path, command = sys.argv[1], sys.argv[2]

    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        sock.connect(path)
    except OSError as exc:
        fail("cannot connect to " + path + ": " + str(exc))

    try:
        # ONE buffered reader for the connection's whole lifetime: a
        # per-call makefile would buffer ahead and silently swallow lines
        # the next reader expects.
        reader = sock.makefile("rb")
        if command == "session":
            session_cmd(reader, path)
            return
        info = read_line(reader)
        if info.get("type") != "session":
            fail("bad handshake: " + json.dumps(info))
        if command == "say":
            if len(sys.argv) < 4:
                fail("say requires text")
            say_cmd(sock, reader, info, sys.argv[3])
        elif command == "query":
            what = sys.argv[3] if len(sys.argv) > 3 else "status"
            send(sock, {"type": "query", "what": what})
            while True:
                event = read_line(reader)
                if event.get("type") == "error":
                    fail("query failed: " + event.get("error", ""))
                if event.get("type") == "query_result" and event.get("what") == what:
                    print(json.dumps(event))
                    return
        elif command == "interrupt":
            send(sock, {"type": "interrupt"})
        elif command == "shutdown":
            send(sock, {"type": "shutdown"})
        else:
            fail("unknown command: " + command)
    finally:
        sock.close()


if __name__ == "__main__":
    main()
