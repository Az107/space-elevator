"""space-elevator function adapter (Python, stdlib only).

Serves an HTTP endpoint that maps each request to an AWS API Gateway
(HTTP API, payload 2.0) proxy event and calls the user's handler:

    def handler(event, context): ...

The handler's return value becomes the HTTP response. Return a dict with
a "statusCode" key to control status/headers/body, or any other value to
be serialized as a 200 JSON response.

Environment:
    SE_FUNCTION_ENTRYPOINT  file:handler  (default "handler.py:handler")
    SE_FUNCTION_APPDIR      directory holding the user code (default /app)
    PORT                    listen port (default 8080)
"""

import base64
import importlib
import json
import os
import sys
import time
import traceback
import urllib.parse
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ENTRY = os.environ.get("SE_FUNCTION_ENTRYPOINT", "handler.py:handler")
APP_DIR = os.environ.get("SE_FUNCTION_APPDIR", "/app")
PORT = int(os.environ.get("PORT", "8080"))

# Request-body ceiling, matching the Node adapter. A function container is
# memory-limited, so an unbounded read lets one large POST OOM-kill it.
MAX_BODY_BYTES = int(os.environ.get("SE_FUNCTION_MAX_BODY", str(32 * 1024 * 1024)))


class RequestError(Exception):
    """A malformed or oversized request, reported with its own status."""

    def __init__(self, status, message):
        super().__init__(message)
        self.status = status
        self.message = message


_handler = None
_handler_err = None


def _parse_entry(entry):
    if ":" in entry:
        file_part, _, symbol = entry.rpartition(":")
    else:
        file_part, symbol = entry, "handler"
    if not symbol:
        symbol = "handler"
    return file_part, symbol


def _load_handler():
    file_part, symbol = _parse_entry(ENTRY)
    module_path = file_part
    if module_path.endswith(".py"):
        module_path = module_path[:-3]
    module_path = module_path.replace("/", ".")
    if APP_DIR not in sys.path:
        sys.path.insert(0, APP_DIR)
    module = importlib.import_module(module_path)
    fn = getattr(module, symbol, None)
    if not callable(fn):
        raise RuntimeError("handler %r not found in %s" % (symbol, file_part))
    return fn


class Context:
    def __init__(self):
        self.function_name = os.environ.get("SE_FUNCTION_NAME", "function")
        self.function_version = "$LATEST"
        self.memory_limit_in_mb = int(os.environ.get("SE_FUNCTION_MEMORY_MB", "512"))
        self.aws_request_id = str(uuid.uuid4())
        self.log_group_name = "/space-elevator/" + self.function_name
        self.log_stream_name = "local"
        self.invoked_function_arn = "arn:aws:lambda:local:0:function:" + self.function_name
        self._deadline = time.time() + float(os.environ.get("SE_FUNCTION_TIMEOUT", "900"))

    def get_remaining_time_in_millis(self):
        return max(0, int((self._deadline - time.time()) * 1000))


class Adapter(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))

    def _read_body(self):
        """Read the request body, bounded like the Node adapter.

        Content-Length is a client-supplied hint, so it is used only to
        reject early; the read itself is still capped. Without the cap a
        single large POST can exhaust the container's memory, which the
        platform then restarts and reports as an error.
        """
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            raise RequestError(400, "invalid Content-Length")
        if length < 0:
            raise RequestError(400, "invalid Content-Length")
        if length > MAX_BODY_BYTES:
            raise RequestError(413, "request body too large")
        if length == 0:
            return b""
        body = self.rfile.read(length)
        # Trust the stream, not the header: a client can understate the
        # length to make us stop early, or the socket can deliver more.
        if len(body) > MAX_BODY_BYTES:
            raise RequestError(413, "request body too large")
        return body

    def _build_event(self):
        body_bytes = self._read_body()
        is_base64 = False
        if body_bytes:
            try:
                body = body_bytes.decode("utf-8")
            except UnicodeDecodeError:
                body = base64.b64encode(body_bytes).decode("ascii")
                is_base64 = True
        else:
            body = None

        parsed = urllib.parse.urlsplit(self.path)
        params = {}
        for key, value in urllib.parse.parse_qsl(parsed.query, keep_blank_values=True):
            if key in params:
                if isinstance(params[key], list):
                    params[key].append(value)
                else:
                    params[key] = [params[key], value]
            else:
                params[key] = value

        return {
            "version": "2.0",
            "routeKey": "$default",
            # payload 2.0 fields plus the payload 1.0 style aliases
            # (httpMethod/path) so either handler convention works.
            "rawPath": parsed.path,
            "rawQueryString": parsed.query,
            "httpMethod": self.command,
            "path": parsed.path,
            "headers": {k: v for k, v in self.headers.items()},
            "queryStringParameters": params or None,
            "body": body,
            "isBase64Encoded": is_base64,
            "requestContext": {
                "accountId": "local",
                "stage": "$default",
                "requestId": str(uuid.uuid4()),
                "http": {
                    "method": self.command,
                    "path": parsed.path,
                    "protocol": self.request_version,
                    "sourceIp": self.client_address[0] if self.client_address else "",
                    "userAgent": self.headers.get("User-Agent", ""),
                },
            },
        }

    def _send(self, status, headers, body_bytes, is_base64=False):
        if isinstance(body_bytes, str):
            body_bytes = body_bytes.encode("utf-8")
        try:
            self.send_response(status)
        except Exception:
            return
        sent_type = False
        if headers:
            for name, value in headers.items():
                lname = name.lower()
                if lname in ("content-length", "transfer-encoding", "connection"):
                    continue
                if lname == "content-type":
                    sent_type = True
                if isinstance(value, list):
                    for item in value:
                        self.send_header(name, item)
                else:
                    self.send_header(name, value)
        if not sent_type:
            self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body_bytes)))
        self.end_headers()
        self.wfile.write(body_bytes)

    def _respond(self, result):
        if isinstance(result, dict) and "statusCode" in result:
            status = int(result.get("statusCode", 200))
            headers = dict(result.get("headers") or {})
            cookies = result.get("cookies")
            if cookies:
                headers.setdefault("Set-Cookie", cookies)
            body = result.get("body", "")
            is_base64 = bool(result.get("isBase64Encoded"))
            if is_base64 and isinstance(body, str):
                body_bytes = base64.b64decode(body)
            elif isinstance(body, (dict, list)):
                body_bytes = json.dumps(body).encode("utf-8")
                headers.setdefault("Content-Type", "application/json")
            else:
                body_bytes = ("" if body is None else str(body)).encode("utf-8")
            self._send(status, headers, body_bytes)
            return
        if result is None:
            self._send(200, {"Content-Type": "application/json"}, b"null")
            return
        self._send(200, {"Content-Type": "application/json"}, json.dumps(result))

    def _handle(self):
        if self.path == "/__se/health":
            if _handler_err is not None:
                self._send(503, {"Content-Type": "application/json"},
                           json.dumps({"status": "error", "error": str(_handler_err)}))
            elif _handler is None:
                self._send(503, {"Content-Type": "application/json"}, b'{"status":"loading"}')
            else:
                self._send(200, {"Content-Type": "application/json"}, b'{"status":"ok"}')
            return
        if _handler is None:
            msg = "handler unavailable: %s" % (_handler_err or "not loaded")
            self._send(500, {"Content-Type": "application/json"},
                       json.dumps({"errorMessage": msg, "errorType": "RuntimeError"}))
            return
        try:
            event = self._build_event()
            result = _handler(event, Context())
            self._respond(result)
        except RequestError as exc:
            # An oversized or malformed request is the caller's problem, not
            # a handler crash: report it as 413/400 rather than 500.
            self._send(exc.status, {"Content-Type": "application/json"},
                       json.dumps({"errorMessage": exc.message, "errorType": "RequestError"}))
        except Exception as exc:  # noqa: BLE001 - surface to the caller
            sys.stderr.write(traceback.format_exc())
            self._send(500, {"Content-Type": "application/json"},
                       json.dumps({"errorMessage": str(exc), "errorType": type(exc).__name__}))

    def do_GET(self):
        self._handle()

    do_POST = do_GET
    do_PUT = do_GET
    do_PATCH = do_GET
    do_DELETE = do_GET
    do_HEAD = do_GET
    do_OPTIONS = do_GET


def main():
    global _handler, _handler_err
    try:
        _handler = _load_handler()
        sys.stderr.write("loaded handler %s\n" % ENTRY)
    except Exception as exc:  # noqa: BLE001 - served as 503 on health
        _handler_err = exc
        sys.stderr.write("failed to load handler %s: %s\n" % (ENTRY, exc))
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Adapter)
    sys.stderr.write("function adapter listening on :%d\n" % PORT)
    server.serve_forever()


if __name__ == "__main__":
    main()
