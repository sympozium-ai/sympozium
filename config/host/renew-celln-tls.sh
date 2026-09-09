#!/usr/bin/env bash
# Private operator-CA leaf renewal. Never restarts the Celln owner/dispatcher.
set -euo pipefail
umask 077
credential_dir=${1:?usage: renew-celln-tls.sh ABS_CREDENTIAL_DIR IPV4}
server_ip=${2:?explicit certificate IP required}
[[ "$credential_dir" == /* && "$credential_dir" != / && -d "$credential_dir" ]]
[[ "$server_ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]
awk -F. '{for(i=1;i<=4;i++) if($i>255) exit 1}' <<<"$server_ip"
exec 9>"$credential_dir/renew.lock"
flock -n 9 || exit 0
# Refuse renewal under a near-expiry CA. CA rotation requires deliberate client
# trust distribution and is never inferred from a failing handshake.
openssl x509 -in "$credential_dir/ca.crt" -checkend 8035200 -noout
if openssl x509 -in "$credential_dir/tls.crt" -checkend 2592000 -noout &&
   openssl verify -CAfile "$credential_dir/ca.crt" -verify_ip "$server_ip" "$credential_dir/tls.crt"; then
  exit 0
fi
renew_dir=$(mktemp -d "$credential_dir/renew.XXXXXX")
trap 'rm -f -- "$renew_dir/request.csr" "$renew_dir/extensions.cnf" "$renew_dir/tls.crt"; rmdir -- "$renew_dir"' EXIT
openssl req -new -key "$credential_dir/tls.key" -subj "/CN=$server_ip" -out "$renew_dir/request.csr"
printf 'subjectAltName=IP:%s\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n' "$server_ip" > "$renew_dir/extensions.cnf"
openssl x509 -req -in "$renew_dir/request.csr" -CA "$credential_dir/ca.crt" -CAkey "$credential_dir/ca.key" \
  -set_serial "0x$(openssl rand -hex 16)" -days 90 -sha256 \
  -extfile "$renew_dir/extensions.cnf" -out "$renew_dir/tls.crt"
openssl verify -CAfile "$credential_dir/ca.crt" -verify_ip "$server_ip" "$renew_dir/tls.crt"
# Reuse the protected key; only the certificate changes, in one atomic rename.
mv -- "$renew_dir/tls.crt" "$credential_dir/tls.crt"
systemctl try-restart sympozium-celln-native-proxy.service sympozium-celln-router-proxy.service
