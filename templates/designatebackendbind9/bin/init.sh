#!/bin/bash
#
# Copyright 2024 Red Hat Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License"); you may
# not use this file except in compliance with the License. You may obtain
# a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
# WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
# License for the specific language governing permissions and limitations
# under the License.
set -ex

# expect that the common.sh is in the same dir as the calling script
SCRIPTPATH="$( cd "$(dirname "$0")" >/dev/null 2>&1 ; pwd -P )"
. ${SCRIPTPATH}/common.sh --source-only

# Calculate predictable IP for this pod
if [[ -n "${NETWORK_ATTACHMENT_DEFINITION}" ]]; then
    echo "Calculating predictable IP for pod..."
    ${SCRIPTPATH}/set-predictable-ip.sh
    if [[ $? -eq 0 ]]; then
        # Source the predictable IP environment variables
        source /var/lib/config-data/merged/predictable-ip.env
        echo "Using predictable IP: ${PREDICTABLE_IP}"

        # Update BIND9 configuration to listen on the specific IP
        if [[ -f "/var/lib/config-data/merged/named/options.conf" ]]; then
            sed -i "s/listen-on port 53 { any; };/listen-on port 53 { ${PREDICTABLE_IP}; };/" /var/lib/config-data/merged/named/options.conf
            echo "Updated options.conf to listen on ${PREDICTABLE_IP}:53"
        fi

        # Update RNDC configuration to use the specific IP
        if [[ -f "/var/lib/config-data/merged/named/rndc.conf" ]]; then
            sed -i "s/inet \* port 953/inet ${PREDICTABLE_IP} port 953/" /var/lib/config-data/merged/named/rndc.conf
            echo "Updated rndc.conf to use ${PREDICTABLE_IP}:953"
        fi
    else
        echo "WARNING: Failed to calculate predictable IP, using default configuration"
    fi
else
    echo "WARNING: NETWORK_ATTACHMENT_DEFINITION not set, using default configuration"
fi

# Merge all templates from config CM
for dir in /var/lib/config-data/default; do
    merge_config_dir ${dir}
done

mkdir /var/lib/config-data/merged/named
cp -f /var/lib/config-data/default/named/* /var/lib/config-data/merged/named/

# Using the index for the podname, get the matching rndc key and copy it into the proper location

if [[ -z "${POD_NAME}" ]]; then
    echo "ERROR: requires the POD_NAME variable to be set"
    exit 1
fi
if [[ -z "${RNDC_PREFIX}" ]]; then
    rndc_prefix="rndc-key-"
else
    rndc_prefix="${RNDC_PREFIX}-"
fi

# get the index off of the pod name
set -f
name_parts=(${POD_NAME//-/ })
pod_index="${name_parts[-1]}"
rndc_key_filename="/var/lib/config-data/keys/${rndc_prefix}${pod_index}"
if [[ -f "${rndc_key_filename}" ]]; then
    cp ${rndc_key_filename} /var/lib/config-data/merged/named/rndc.key
else
    echo "ERROR: rndc key not found!"
    exit 1
fi
