#!/bin/bash

set -ex

function control {
    bin/hhfab vlab ssh --vm control-1 PATH=/opt/bin "$@"
}

control kubectl fabric vpc create --name vpc-1 --subnet 10.90.1.1/24
control kubectl fabric vpc create --name vpc-2 --subnet 10.90.2.1/24

control kubectl fabric vpc attach --vpc vpc-1 --conn server-1--mclag--switch-1--switch-2
control kubectl fabric vpc attach --vpc vpc-2 --conn server-2--mclag--switch-1--switch-2

control kubectl fabric vpc peer --vpc vpc-1 --vpc vpc-2

control kubectl get agent -o wide
control kubectl wait --for=condition=Applied agent/switch-1 agent/switch-2 --timeout=600s