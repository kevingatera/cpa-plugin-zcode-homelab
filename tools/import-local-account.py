#!/usr/bin/env python3
"""Import an existing ZCode desktop OAuth account without printing credentials."""
import argparse
import base64
import getpass
import hashlib
import json
import os
from pathlib import Path
import subprocess
import urllib.request
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--plan", choices=["start", "individual"], default="start")
args = parser.parse_args()

home = Path.home()
credentials = json.loads((home / '.zcode/v2/credentials.json').read_text())
if args.plan == 'individual':
    names = [k for k in credentials if 'individual-coding-plan' in k and k.endswith(':api-key')]
    if len(names) != 1:
        raise SystemExit('Expected exactly one native individual Coding Plan credential.')
    value = credentials[names[0]]
else:
    value = credentials['zcodejwttoken']
if value.startswith('enc:v1:'):
    iv, tag, body = value[7:].split('.')
    decode = lambda v: base64.urlsafe_b64decode(v + '=' * (-len(v) % 4))
    secret = os.environ.get('ZCODE_CREDENTIAL_SECRET') or f'zcode-credential-fallback:linux:{home}:{getpass.getuser()}'
    value = AESGCM(hashlib.sha256(secret.encode()).digest()).decrypt(decode(iv), decode(body) + decode(tag), None).decode()
telemetry = json.loads((home / '.zcode/v2/telemetry-state.json').read_text())
account = {'type': 'zcode', 'access_token': value, 'device_mid': telemetry['deviceMid'], 'prefix': 'zcode', 'email': 'ZCode workstation account'}
name = 'zcode-workstation.json'
if args.plan == 'individual':
    name = 'zcode-individual-workstation.json'
    account = {'type': 'zcode', 'auth_kind': 'apikey', 'api_key': value, 'device_mid': telemetry['deviceMid'], 'prefix': 'zcode-individual', 'email': 'Z.ai individual Coding Plan'}
key = subprocess.check_output(['secret-tool', 'lookup', 'application', 'cliproxy', 'host', 'omv-108', 'service', 'management-ui']).decode().strip()
request = urllib.request.Request('https://cliproxy.deployitwith.me/v8/management/credentials?name=' + name, data=json.dumps(account).encode(), headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'}, method='POST')
with urllib.request.urlopen(request) as response:
    result = json.load(response)
print(args.plan + ' account imported:', result.get('status', 'ok'))
