#!/usr/bin/env python3
"""Validate the real Compose overlay using isolated, synthetic local services.

By default only inspect merged configuration. --runtime starts a uniquely named
project and removes only that project's containers/volumes in finally. Images
must already exist locally; no official credentials or generation calls are used.
"""
import argparse
import base64
import http.client
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid


ROOT = Path(__file__).resolve().parents[2]
KEY_PATH = "/run/codex4server/service_key"
ADMIN_EMAIL = "service-key-test@example.invalid"
ADMIN_PASSWORD = "LocalSyntheticServiceKey119"


def run(args, *, env=None, input=None, check=True):
    result = subprocess.run(args, input=input, capture_output=True, env=env)
    if check and result.returncode:
        # Do not print stdin, environment or Docker logs: they may contain keys.
        raise RuntimeError(f"command failed ({result.returncode}): {args[0]} {args[1]}")
    return result


def eventually(description, check, timeout=120):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, ValueError, RuntimeError, urllib.error.URLError, http.client.HTTPException):
            pass
        time.sleep(1)
    raise AssertionError(f"timed out: {description}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime", action="store_true")
    parser.add_argument("--image", default="sub4api:1.1.9-gateway-local")
    args = parser.parse_args()
    project = "sub4api-key-test-" + uuid.uuid4().hex[:10]
    env = os.environ.copy()
    env.update({
        "SUB4API_IMAGE": args.image,
        "CODEX_GATEWAY_SOURCE_DIR": env.get("CODEX_GATEWAY_SOURCE_DIR", str(ROOT.parent / "codex4server")),
        "POSTGRES_USER": "key_test", "POSTGRES_DB": "key_test",
        "POSTGRES_PASSWORD": "local-synthetic-key-test",
        "REDIS_PASSWORD": "", "JWT_SECRET": "local-key-test-jwt-secret-keep-fixed-119",
        "ADMIN_EMAIL": ADMIN_EMAIL, "ADMIN_PASSWORD": ADMIN_PASSWORD,
        "BIND_HOST": "127.0.0.1", "CODEX_GATEWAY_UID": "1000", "CODEX_GATEWAY_GID": "1000",
    })
    env.pop("CODEX_GATEWAY_SERVICE_KEY_FILE", None)
    with tempfile.TemporaryDirectory(prefix="sub4api-key-compose-") as directory:
        override = Path(directory) / "test.yml"
        legacy = Path(directory) / "legacy.py"
        legacy.write_text('''import json,time,uuid
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_POST(self):
  request=json.loads(self.rfile.read(int(self.headers.get('Content-Length',0))))
  response={'id':'resp_keytest_'+uuid.uuid4().hex,'object':'response','model':request.get('model','gpt-5.5'),'status':'completed','output':[{'id':'msg_keytest','type':'message','role':'assistant','content':[{'type':'output_text','text':'OK'}]}],'usage':{'input_tokens':2,'output_tokens':1,'total_tokens':3}}
  if request.get('stream'):
   events=[{'type':'response.created','response':{'id':response['id'],'model':response['model']}},{'type':'response.output_text.delta','delta':'OK'},{'type':'response.output_item.done','item':response['output'][0]},{'type':'response.completed','response':response}]
   data=b''.join(('data: '+json.dumps(event)+'\\n\\n').encode() for event in events);content='text/event-stream'
  else:data=json.dumps(response).encode();content='application/json'
  self.send_response(200);self.send_header('Content-Type',content);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
ThreadingHTTPServer(('0.0.0.0',8080),Handler).serve_forever()
''')
        override.write_text(f'''services:
  sub2api:
    container_name: !reset null
    ports: !reset []
    volumes: !override
      - type: volume
        target: /app/data
      - type: volume
        source: codex_gateway_service_key
        target: /run/codex4server
    environment:
      GATEWAY_CODEX4SERVER_SYNC_INTERVAL: 2s
      GATEWAY_OPENAI_CODEX_TICKET_ENABLED: "false"
  postgres:
    container_name: !reset null
    healthcheck:
      interval: 1s
  redis:
    image: redis:8.4-alpine
    container_name: !reset null
    healthcheck:
      interval: 1s
  legacy-upstream:
    image: python:3.12-slim-bookworm
    command: ["python", "/fixture.py"]
    volumes:
      - "{legacy}:/fixture.py:ro"
    networks: [sub2api-network]
networks:
  sub2api-network:
    internal: true
''')
        command = ["docker", "compose", "--project-name", project,
                   "-f", str(ROOT / "deploy/docker-compose.yml"),
                   "-f", str(ROOT / "deploy/docker-compose.codex-gateway.yml"),
                   "-f", str(override)]

        def compose(*arguments, input=None, check=True):
            return run(command + list(arguments), env=env, input=input, check=check)

        config = json.loads(compose("config", "--format", "json").stdout)
        services = config["services"]
        app, gateway = services["sub2api"], services["gateway"]
        assert app["environment"]["GATEWAY_CODEX4SERVER_AUTO_GENERATE_SERVICE_KEY"] == "true"
        assert app["environment"]["GATEWAY_CODEX4SERVER_SERVICE_KEY_FILE"] == KEY_PATH
        assert KEY_PATH in gateway["command"]
        assert "gateway" not in app.get("depends_on", {})
        assert not gateway.get("ports")
        assert set(app["networks"]) & set(gateway["networks"]) & set(services["postgres"]["networks"])
        app_mount = next(v for v in app["volumes"] if v["target"] == "/run/codex4server")
        gateway_mount = next(v for v in gateway["volumes"] if v["target"] == "/run/codex4server")
        assert app_mount["source"] == gateway_mount["source"] == "codex_gateway_service_key"
        assert not app_mount.get("read_only") and gateway_mount["read_only"]
        assert not app.get("secrets") and not gateway.get("secrets")
        print("merged Compose: automatic persistent key, correct network, no public Gateway port", flush=True)
        if not args.runtime:
            return

        def app_urls():
            ids = compose("ps", "--all", "--quiet", "sub2api").stdout.decode().split()
            if len(ids) != 2:
                raise RuntimeError("both test instances must exist")
            containers = json.loads(run(["docker", "inspect", *ids]).stdout)
            urls = []
            for container in containers:
                networks = container["NetworkSettings"]["Networks"].values()
                address = next((n["IPAddress"] for n in networks if n.get("IPAddress")), None)
                if not address:
                    raise RuntimeError("test instance network is not assigned yet")
                urls.append("http://" + address + ":8080")
            return urls

        def request(base, path, data=None, token=None, method=None):
            headers = {"Content-Type": "application/json", "Idempotency-Key": str(uuid.uuid4())}
            if token:
                headers["Authorization"] = "Bearer " + token
            # The test network has no Internet route or published host ports.
            # Run the HTTP client inside the synthetic upstream container.
            client = '''import sys,json,base64,urllib.request,urllib.error
v=json.load(sys.stdin)
request=urllib.request.Request(v['url'],headers=v['headers'],method=v['method'],data=None if v['data'] is None else json.dumps(v['data']).encode())
try:
 with urllib.request.urlopen(request,timeout=15) as r: status,body=r.status,r.read()
except urllib.error.HTTPError as e:status,body=e.code,e.read()
print(json.dumps({'status':status,'body':base64.b64encode(body).decode()}))
'''
            payload = json.dumps({"url": base + path, "headers": headers, "data": data, "method": method}).encode()
            response = json.loads(compose("exec", "-T", "legacy-upstream", "python", "-c", client, input=payload).stdout)
            return response["status"], base64.b64decode(response["body"])

        def app_health():
            return all(request(base, "/health")[0] == 200 for base in app_urls())

        def keys():
            return [compose("exec", "-T", "--index", str(index), "sub2api", "cat", KEY_PATH).stdout.strip()
                    for index in (1, 2)]

        def gateway_status(key=None):
            settings = 'url = "http://127.0.0.1:8787/internal/capabilities"\n'
            if key is not None:
                settings += 'header = "Authorization: Bearer ' + key.decode() + '"\n'
            return compose("exec", "-T", "gateway", "curl", "--max-time", "4", "--silent", "--show-error",
                           "--output", "/dev/null", "--write-out", "%{http_code}", "--config", "-",
                           input=settings.encode()).stdout == b"200"

        def gateway_rejected(key=None):
            settings = 'url = "http://127.0.0.1:8787/internal/capabilities"\n'
            if key is not None:
                settings += 'header = "Authorization: Bearer ' + key.decode() + '"\n'
            return compose("exec", "-T", "gateway", "curl", "--max-time", "4", "--silent", "--show-error",
                           "--output", "/dev/null", "--write-out", "%{http_code}", "--config", "-",
                           input=settings.encode()).stdout == b"401"

        def edit_fixture_key(body, mode):
            compose("run", "--rm", "--no-deps", "-T", "--entrypoint", "sh", "sub2api", "-c",
                    'cat > /run/codex4server/service_key && chmod "$1" /run/codex4server/service_key',
                    "sh", mode, input=body)

        try:
            # The early Gateway process must restart until Sub4API creates the file.
            compose("up", "-d", "--no-build", "--pull", "never", "postgres", "redis", "gateway", "legacy-upstream")
            eventually("Gateway waiting for first key", lambda: b"Cannot read Gateway service key file" in
                       compose("logs", "--no-color", "gateway").stdout, timeout=40)
            compose("up", "-d", "--no-build", "--pull", "never", "--scale", "sub2api=2", "sub2api")
            eventually("both Sub4API instances healthy", app_health)
            first, second = keys()
            assert first == second and len(bytes.fromhex(first.decode())) == 32
            eventually("Gateway reads shared generated key", lambda: gateway_status(first))
            assert gateway_rejected() and gateway_rejected(b"intentionally-wrong-key")
            for service in ("sub2api", "gateway"):
                modes = compose("exec", "-T", service, "stat", "-c", "%a:%u:%g", "/run/codex4server", KEY_PATH).stdout.splitlines()
                assert modes == [b"700:1000:1000", b"600:1000:1000"]
            assert compose("exec", "-T", "gateway", "sh", "-c", "test ! -w /run/codex4server").returncode == 0
            print("first start: two instances share one private key; Gateway recovered; missing/wrong keys rejected", flush=True)

            urls = app_urls()
            status, raw = request(urls[0], "/api/v1/auth/login", {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
            assert status == 200, "synthetic administrator login failed"
            token = json.loads(raw)["data"]["access_token"]
            # Test-only acknowledgement for the synthetic administrator, not a real operator's consent.
            sql = """INSERT INTO settings(key,value,updated_at)
SELECT 'admin_compliance_acknowledgement:'||id,
json_build_object('version','v2026.06.10','admin_user_id',id,'user_agent','synthetic Compose key fixture','accepted_at',NOW())::text,NOW()
FROM users WHERE email='service-key-test@example.invalid'
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value;"""
            compose("exec", "-T", "postgres", "psql", "-U", "key_test", "-d", "key_test", "-v", "ON_ERROR_STOP=1", input=sql.encode())

            def admin(path, data=None):
                status, raw = request(app_urls()[0], "/api/v1" + path, data, token)
                assert status == 200, f"synthetic admin request failed: {path} HTTP {status}"
                return json.loads(raw)["data"]

            group = admin("/admin/groups", {"name": "Legacy key fixture", "platform": "openai", "rate_multiplier": 1})
            admin("/admin/accounts", {"name": "Legacy key fixture", "platform": "openai", "type": "apikey",
                                      "credentials": {"api_key": "synthetic-local-only", "base_url": "http://legacy-upstream:8080"},
                                      "concurrency": 2, "priority": 50, "group_ids": [group["id"]]})
            admin("/admin/users/1/balance", {"balance": 10, "operation": "add", "notes": "Synthetic Compose fixture"})
            api_key = admin("/keys", {"name": "Legacy fixture", "group_id": group["id"]})["key"]

            def legacy_request():
                for base in app_urls():
                    status, raw = request(base, "/v1/responses", {"model": "gpt-5.5", "input": "Reply OK", "stream": False}, api_key)
                    assert status == 200, f"legacy synthetic request HTTP {status}"
                    assert json.loads(raw)["status"] == "completed"

            codex_group = admin("/admin/groups", {"name": "Codex key fixture", "platform": "openai_codex", "rate_multiplier": 1})
            def encoded(value):
                return base64.urlsafe_b64encode(json.dumps(value).encode()).decode().rstrip("=")
            access = encoded({"alg": "none"}) + "." + encoded({"exp": int(time.time()) + 86400,
                "https://api.openai.com/auth": {"chatgpt_account_id": "key-fixture"}}) + ".fixture"
            account = admin("/admin/accounts", {"name": "Codex key fixture", "platform": "openai_codex", "type": "gateway",
                "gateway_credentials": {"type": "tokens", "access_token": access, "chatgpt_account_id": "key-fixture"},
                "credentials": {}, "concurrency": 1, "priority": 50, "group_ids": [codex_group["id"]]})
            assert account["gateway"]["service_available"] and account["gateway"]["sync_state"] == "ready"
            legacy_request()

            compose("restart", "sub2api", "gateway")
            eventually("restart health", app_health)
            assert keys() == [first, first]
            eventually("restart Gateway", lambda: gateway_status(first))
            compose("up", "-d", "--no-build", "--pull", "never", "--force-recreate", "--scale", "sub2api=2", "sub2api", "gateway")
            eventually("recreated instances healthy", app_health)
            assert keys() == [first, first]
            eventually("recreated Gateway", lambda: gateway_status(first))
            print("restart/recreate: key unchanged; account management and legacy requests succeeded", flush=True)

            for kind in ("invalid", "unreadable"):
                compose("stop", "sub2api", "gateway")
                body = b"invalid-key" if kind == "invalid" else first + b"\n"
                edit_fixture_key(body, "600" if kind == "invalid" else "000")
                compose("up", "-d", "--no-build", "--pull", "never", "--scale", "sub2api=2", "sub2api")
                eventually(kind + " key does not prevent startup", app_health)
                actual = compose("exec", "-T", "--user", "0", "sub2api", "cat", KEY_PATH).stdout
                assert actual == body, "existing invalid/unreadable key was overwritten"
                legacy_request()
                status = admin(f"/admin/accounts/{account['id']}")["gateway"]
                assert status["service_available"] is False
                assert first not in compose("logs", "--no-color", "sub2api", "gateway").stdout
                print(kind + " key: preserved; OpenAI Codex unavailable; both legacy instances still return 200", flush=True)
            compose("stop", "sub2api")
            edit_fixture_key(first + b"\n", "600")
            compose("up", "-d", "--no-build", "--pull", "never", "--scale", "sub2api=2", "sub2api", "gateway")
            eventually("recovery after fixture repair", app_health)
            eventually("Gateway key restored", lambda: gateway_status(first))
            assert keys() == [first, first]
            print("runtime Compose key validation passed; no official requests", flush=True)
        finally:
            # Only the uniquely named project created above is removed.
            compose("down", "--volumes", "--remove-orphans")


if __name__ == "__main__":
    main()
