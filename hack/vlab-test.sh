#!/bin/bash

set -ex

function control {
    bin/hhfab vlab ssh --vm control-1 PATH=/opt/bin "$@"
}

control kubectl fabric vpc create --name vpc-1 --subnet 10.90.0.1/24
control kubectl fabric vpc attach --vpc vpc-1 --conn server-1--mclag--switch-1--switch-2
control kubectl fabric vpc attach --vpc vpc-1 --conn server-2--mclag--switch-1--switch-2

control kubectl get agent -o wide