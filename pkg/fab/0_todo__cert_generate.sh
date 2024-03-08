#!/bin/bash
# Copyright 2023 Hedgehog
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


# TODO replace with go code

set -e

OPENSSL=$(which openssl)

cat <<EOT > openssl.cnf
[ ca_cert ]
basicConstraints=CA:TRUE
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid:always,issuer

[ server_cert ]
basicConstraints=CA:FALSE
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid,issuer
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = critical,serverAuth

[ client_cert ]
basicConstraints=CA:FALSE
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid,issuer
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = critical,clientAuth

[ server_client_cert ]
basicConstraints=CA:FALSE
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid,issuer
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = critical,serverAuth,clientAuth

[ code_sign_cert ]
basicConstraints=CA:FALSE
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid,issuer
keyUsage = critical,digitalSignature
EOT

### REGISTRY

# create CAs
${OPENSSL} ecparam -name prime256v1 -genkey -noout -out oci-repo-ca-key.pem
${OPENSSL} req -new -nodes -x509 -days 3600 -config openssl.cnf -extensions ca_cert -key oci-repo-ca-key.pem -out oci-repo-ca-cert.pem -subj "/C=US/ST=Washington/L=Seattle/O=Hedgehog SONiC Foundation/CN=OCI Repository CA"

# create a server cert
SANS="DNS:localhost, DNS:registry.local, IP:127.0.0.1, IP:10.0.2.2"
${OPENSSL} ecparam -name prime256v1 -genkey -noout -out oci-server-key.pem
${OPENSSL} req -new -nodes -x509 -days 360 \
  -CAkey oci-repo-ca-key.pem -CA oci-repo-ca-cert.pem \
  -key oci-server-key.pem -out oci-server-cert.pem \
  -config openssl.cnf -extensions server_cert \
  -addext "subjectAltName = ${SANS}" \
  -subj "/C=US/ST=Washington/L=Seattle/O=Hedgehog SONiC Foundation/CN=localhost"

### DAS BOOT

# create CAs
${OPENSSL} ecparam -name prime256v1 -genkey -noout -out das-boot-server-ca-key.pem
${OPENSSL} req -new -nodes -x509 -days 3600 -config openssl.cnf -extensions ca_cert -key das-boot-server-ca-key.pem -out das-boot-server-ca-cert.pem -subj "/C=US/ST=Washington/L=Seattle/O=Hedgehog SONiC Foundation/CN=DAS BOOT Server CA"

${OPENSSL} ecparam -name prime256v1 -genkey -noout -out das-boot-client-ca-key.pem
${OPENSSL} req -new -nodes -x509 -days 3600 -config openssl.cnf -extensions ca_cert -key das-boot-client-ca-key.pem -out das-boot-client-ca-cert.pem -subj "/C=US/ST=Washington/L=Seattle/O=Hedgehog SONiC Foundation/CN=DAS BOOT Client CA"

${OPENSSL} ecparam -name prime256v1 -genkey -noout -out das-boot-config-ca-key.pem
${OPENSSL} req -new -nodes -x509 -days 3600 -config openssl.cnf -extensions ca_cert -key das-boot-config-ca-key.pem -out das-boot-config-ca-cert.pem -subj "/C=US/ST=Washington/L=Seattle/O=Hedgehog SONiC Foundation/CN=DAS BOOT Config Signatures CA"

# create a server cert
SANS="DNS:localhost, DNS:das-boot.hedgehog.svc.cluster.local, DNS:hedgehog-seeder-das-boot-seeder.default.svc.cluster.local, DNS:hh-seeder-das-boot-seeder.default.svc.cluster.local, IP:127.0.0.1, IP:10.43.42.42, IP:192.168.42.1"
${OPENSSL} ecparam -name prime256v1 -genkey -noout -out das-boot-server-key.pem
${OPENSSL} req -new -nodes -x509 -days 360 \
  -CAkey das-boot-server-ca-key.pem -CA das-boot-server-ca-cert.pem \
  -key das-boot-server-key.pem -out das-boot-server-cert.pem \
  -config openssl.cnf -extensions server_cert \
  -addext "subjectAltName = ${SANS}" \
  -subj "/C=US/ST=Washington/L=Seattle/O=Hedgehog SONiC Foundation/CN=localhost"

# create a config signing cert
${OPENSSL} ecparam -name prime256v1 -genkey -noout -out das-boot-config-key.pem
${OPENSSL} req -new -nodes -x509 -days 360 \
  -CAkey das-boot-config-ca-key.pem -CA das-boot-config-ca-cert.pem \
  -key das-boot-config-key.pem -out das-boot-config-cert.pem \
  -config openssl.cnf -extensions code_sign_cert \
  -subj "/C=US/ST=Washington/L=Seattle/O=Hedgehog SONiC Foundation/CN=Embedded Config Generator"
