#!/usr/bin/env bash
# Generate a test CA and a certificate for localhost. Use these only for development and CI.
set -euo pipefail
cd "$(dirname "$0")"
openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj "/CN=pgmux test CA" \
  -keyout ca.key -out ca.crt 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj "/CN=localhost" -keyout server.key -out server.csr 2>/dev/null
printf "subjectAltName=DNS:localhost,DNS:postgres,DNS:pgmux,IP:127.0.0.1\n" > san.ext
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 3650 \
  -extfile san.ext -out server.crt 2>/dev/null
rm -f server.csr san.ext ca.srl
chmod 600 server.key ca.key
echo "wrote ca.crt, server.crt and server.key in $(pwd)"
