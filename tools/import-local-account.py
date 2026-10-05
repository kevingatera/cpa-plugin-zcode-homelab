#!/usr/bin/env python3
"""Import an existing ZCode desktop OAuth account without printing credentials."""
import base64
import getpass
import hashlib
import json
import os
from pathlib import Path
import subprocess
import urllib.request
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

home = Path.home()
credentials = json.loads((home / '.zcode/v2/credentials.json').read_text())
value = credentials['zcodejwttoken']
if value.startswith('enc:v1:'):
    iv, tag, body = value[7:].split('.')
    decode = lambda v: base64.urlsafe_b64decode(v + '=' * (-len(v) % 4))
    secret = os.environ.get('ZCODE_CREDENTIAL_SECRET') or f'zcode-credential-fallback:linux:{home}:{getpass.getuser()}'
    value = AESGCM(hashlib.sha256(secret.encode()).digest()).decrypt(decode(iv), decode(body) + decode(tag), None).decode()
telemetry = json.loads((home / '.zcode/v2/telemetry-state.json').read_text())
account = {'type': 'zcode', 'access_token': value, 'device_mid': telemetry['deviceMid'], 'prefix': 'zcode', 'email': 'ZCode workstation account'}
key = subprocess.check_output(['secret-tool', 'lookup', 'application', 'cliproxy', 'host', 'omv-108', 'service', 'management-ui']).decode().strip()
request = urllib.request.Request('http://192.168.1.108:8317/v8/management/credentials?name=zcode-workstation.json', data=json.dumps(account).encode(), headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'}, method='POST')
with urllib.request.urlopen(request) as response:
    result = json.load(response)
print('OAuth account imported:', result.get('status', 'ok'))
