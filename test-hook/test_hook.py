#!/usr/bin/env python3
import sys
import json
import os

LOG = "/tmp/picoclaw-hook.log"

def log(msg):
    with open(LOG, "a") as f:
        f.write(msg + "\n")

# Clear log
if os.path.exists(LOG):
    os.remove(LOG)

log("===HOOK STARTED===")

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        log(f"NON-JSON: {line}")
        continue
    method = msg.get("method", "")
    msg_id = msg.get("id")
    params = msg.get("params", {})

    if method == "hook.hello":
        log(f"HELLO received: {json.dumps(params)[:200]}")
        # Respond
        result = {"jsonrpc": "2.0", "id": msg_id, "result": {"ok": True}}
        print(json.dumps(result), flush=True)
        continue

    if method == "hook.runtime_event":
        ev = params.get("event", {})
        kind = ev.get("kind", "?")
        scope = ev.get("scope", {})
        payload = ev.get("payload", {})
        # Truncate payload
        payload_str = json.dumps(payload, ensure_ascii=False)[:300]
        log(f"EVENT kind={kind} chat={scope.get('chat_id', '-')} model={payload.get('model_name', '')} content_len={payload.get('content_delta_len', 0)} reasoning_len={payload.get('reasoning_delta_len', 0)} payload={payload_str}")
        # No response needed
        continue

    log(f"UNKNOWN msg: {json.dumps(msg)[:300]}")

log("===HOOK EXIT===")
