#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0


set -euo pipefail

rm -rf .zot || true
mkdir -p .zot/data

cat > .zot/config.json <<- EOM
{
  "log": {
    "level": "debug"
  },
  "storage": {
    "rootDirectory": "$(pwd)/.zot/data"
  },
  "http": {
    "address": "127.0.0.1",
    "port": "30000"
  },
  "extensions": {
    "sync": {
      "enable": true,
      "credentialsFile": "$(pwd)/.zot/creds.json",
      "registries": [
        {
          "urls": [
            "https://${LOCALREG_SYNC_REGISTRY:-$REGISTRY_URL}"
          ],
          "onDemand": true,
          "tlsVerify": true,
          "content": [
            {
              "prefix": "/githedgehog/**",
              "destination": "/githedgehog",
              "stripPrefix": true
            }
          ]
        }
      ]
    }
  }
}
EOM

cat > .zot/creds.json <<- EOM
{
  "${LOCALREG_SYNC_REGISTRY:-$REGISTRY_URL}": {
    "username": "${LOCALREG_SYNC_USERNAME:-$REGISTRY_USERNAME}",
    "password": "${LOCALREG_SYNC_PASSWORD:-$REGISTRY_PASSWORD}"
  }
}
EOM