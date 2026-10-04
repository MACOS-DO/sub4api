"""Isolated TLS issuer for container tests. Never forwards to a real host.

Only use synthetic accounts with this fixture. Authorization is never captured.
The CONNECT listener terminates TLS locally so the real Rust Gateway still
exercises its account proxy, TLS, cookie, identity and native protocol paths.
"""
import base64
import hashlib
import json
import ssl
import struct
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

tls_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
tls_context.load_cert_chain('/fixtures/server.pem', '/fixtures/server.key')
lock = threading.Lock()
sequence = 0
PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII='


def record(method, path, headers, body=None):
    global sequence
    with lock:
        sequence += 1
        item = {'sequence': sequence, 'method': method, 'path': path}
        item['headers'] = {key: headers.get(key) for key in (
            'session-id', 'thread-id', 'x-codex-installation-id', 'x-codex-turn-id',
            'x-codex-turn-state', 'cookie', 'user-agent') if headers.get(key)}
        if body is not None:
            item['model'] = body.get('model')
            item['client_metadata'] = body.get('client_metadata')
            text = json.dumps(body.get('input', [])) + str(body.get('instructions', ''))
            item['timezone_tokyo'] = 'Asia/Tokyo' in text
            item['timezone_original'] = 'America/New_York' in text
            item['image_bytes'] = sum(len(image.get('image_url', '')) for image in body.get('images', []))
            item['transport_extensions_removed'] = 'codex4server_identity' not in body
        with Path('/captures/requests.jsonl').open('a') as output:
            output.write(json.dumps(item) + '\n')
        return sequence


def events(body, number):
    response_id = f'resp_mock_{number}'
    compact = any(isinstance(item, dict) and item.get('type') == 'compaction_trigger'
                  for item in body.get('input', []) if isinstance(body.get('input'), list))
    output = ([{'id': f'cmp_{number}', 'type': 'compaction', 'encrypted_content': 'opaque-compact'}]
              if compact else [{'id': f'msg_{number}', 'type': 'message', 'role': 'assistant',
                                'status': 'completed', 'content': [{'type': 'output_text', 'text': 'OK', 'annotations': []}]}])
    response = {'id': response_id, 'object': 'response', 'model': body.get('model', 'gpt-5.5'),
                'status': 'completed', 'output': output, 'service_tier': body.get('service_tier', 'default'),
                'usage': {'input_tokens': 10, 'output_tokens': 5, 'total_tokens': 15,
                          'input_tokens_details': {'cached_tokens': 3}},
                'future_extension': {'opaque': 'preserved'}}
    yield {'type': 'response.created', 'response': {'id': response_id, 'model': response['model'], 'status': 'in_progress'}}
    yield {'type': 'future.mock_event', 'opaque': {'preserved': True}}
    if not compact:
        yield {'type': 'response.output_text.delta', 'response_id': response_id, 'output_index': 0, 'content_index': 0, 'delta': 'OK'}
    yield {'type': 'response.output_item.done', 'response_id': response_id, 'output_index': 0, 'item': output[0]}
    yield {'type': 'response.completed', 'response': response}


class Issuer(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *args):
        pass

    def reply(self, payload, status=200, content_type='application/json', headers=None):
        raw = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(raw)))
        self.send_header('X-Request-Id', 'mock-issuer-request')
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.headers.get('Upgrade', '').lower() == 'websocket':
            return self.websocket()
        record('GET', self.path, self.headers)
        if '/models' in self.path:
            if self.headers.get('If-None-Match') == '"mock-catalog-v1"':
                return self.reply(b'', 304, headers={'ETag': '"mock-catalog-v1"'})
            return self.reply({'models': [{'slug': 'gpt-5.5', 'display_name': 'Mock GPT', 'visibility': 'list',
                                          'supported_in_api': True, 'priority': 0,
                                          'supported_reasoning_levels': [{'effort': 'medium', 'description': 'Standard'}]}],
                               'future_catalog': True}, headers={'ETag': '"mock-catalog-v1"'})
        if '/accounts/check/' in self.path:
            binding = self.headers.get('chatgpt-account-id', 'synthetic-account')
            return self.reply({'accounts': {binding: {'account': {'account_id': binding, 'plan_type': 'plus'}, 'user': {'email': 'synthetic@example.invalid', 'id': 'synthetic-user'}}}})
        if '/subscriptions' in self.path:
            return self.reply({'active_until': '2027-10-02T00:00:00Z'})
        if '/wham/usage' in self.path:
            return self.reply({'plan_type': 'plus', 'rate_limit': {'allowed': True, 'limit_reached': False,
                'primary_window': {'used_percent': 10, 'limit_window_seconds': 18000, 'reset_after_seconds': 3600}},
                'credits': {'has_credits': False, 'unlimited': False, 'balance': None}})
        if 'rate-limit-reset-credits' in self.path:
            return self.reply({'available_count': 0, 'credits': []})
        if 'referrals' in self.path:
            return self.reply({'should_show': False, 'remaining_send_capacity': 0})
        return self.reply({'mock_upstream': True})

    def do_POST(self):
        size = int(self.headers.get('Content-Length', '0'))
        if size > 268435456:
            return self.reply({'error': {'message': 'too large'}}, 413)
        raw = self.rfile.read(size)
        if self.headers.get('Content-Encoding') == 'zstd':
            return self.reply({'error': {'message': 'fixture expects uncompressed requests'}}, 415)
        try:
            body = json.loads(raw)
        except ValueError:
            return self.reply({'error': {'message': 'invalid fixture JSON'}}, 400)
        number = record('POST', self.path, self.headers, body)
        if self.path.endswith('/input_tokens'):
            return self.reply({'object': 'response.input_tokens', 'input_tokens': 7})
        if '/images/' in self.path:
            return self.reply({'created': int(time.time()), 'data': [{'b64_json': PNG}],
                               'usage': {'input_tokens': 10, 'output_tokens': 5}, 'future_image': True})
        if 'sdp' in body:
            return self.reply(b'v=0\r\no=mock 1 1 IN IP4 127.0.0.1\r\ns=mock\r\nt=0 0\r\n', 201,
                              'application/sdp', {'Location': f'/backend-api/codex/realtime/calls/rtc_mock_{number}'})
        if '/alpha/search' in self.path:
            return self.reply({'results': [], 'future_search': True})
        data = b''.join(('event: ' + item['type'] + '\ndata: ' + json.dumps(item) + '\n\n').encode()
                        for item in events(body, number))
        return self.reply(data, content_type='text/event-stream', headers={
            'Set-Cookie': 'oai-lb=mock-ticket; Path=/; Secure', 'x-codex-turn-state': 'mock-turn-state'})

    def write_frame(self, opcode, payload):
        size = len(payload)
        header = bytes([128 | opcode])
        header += bytes([size]) if size < 126 else b'\x7e' + struct.pack('!H', size) if size < 65536 else b'\x7f' + struct.pack('!Q', size)
        self.wfile.write(header + payload)
        self.wfile.flush()

    def websocket(self):
        record('WS', self.path, self.headers)
        key = self.headers['Sec-WebSocket-Key'] + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11'
        self.send_response(101)
        self.send_header('Upgrade', 'websocket')
        self.send_header('Connection', 'Upgrade')
        self.send_header('Sec-WebSocket-Accept', base64.b64encode(hashlib.sha1(key.encode()).digest()).decode())
        self.send_header('X-Request-Id', 'mock-ws-request')
        self.end_headers()
        while True:
            frame = self.rfile.read(2)
            if len(frame) != 2:
                return
            opcode, length = frame[0] & 15, frame[1] & 127
            if length == 126:
                length = struct.unpack('!H', self.rfile.read(2))[0]
            elif length == 127:
                length = struct.unpack('!Q', self.rfile.read(8))[0]
            mask = self.rfile.read(4) if frame[1] & 128 else b''
            payload = self.rfile.read(length)
            if mask:
                payload = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
            if opcode == 8:
                self.write_frame(8, payload)
                return
            if opcode == 9:
                self.write_frame(10, payload)
            elif opcode == 1:
                body = json.loads(payload)
                number = record('WS_MESSAGE', self.path, self.headers, body)
                if body.get('type') == 'response.create':
                    for item in events(body, number):
                        self.write_frame(1, json.dumps(item).encode())


class Connect(Issuer):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *args):
        pass

    def do_CONNECT(self):
        if self.path not in {'chatgpt.com:443', 'api.openai.com:443', 'auth.openai.com:443'}:
            self.send_error(403)
            return
        self.send_response(200, 'Connection Established')
        self.end_headers()
        self.wfile.flush()
        try:
            connection = tls_context.wrap_socket(self.connection, server_side=True)
            Issuer(connection, self.client_address, self.server)
        except (OSError, ValueError, ssl.SSLError):
            pass
        self.close_connection = True


ThreadingHTTPServer(('0.0.0.0', 8080), Connect).serve_forever()
