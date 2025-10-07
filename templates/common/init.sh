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

# This script generates the designate.conf file and
# copies the result to the ephemeral /var/lib/config-data/merged volume.
SVC_CFG=/etc/designate/designate.conf
SVC_CFG_MERGED=/var/lib/config-data/merged/designate.conf

# expect that the common.sh is in the same dir as the calling script
SCRIPTPATH="$( cd "$(dirname "$0")" >/dev/null 2>&1 ; pwd -P )"
. ${SCRIPTPATH}/common.sh --source-only

cp -a ${SVC_CFG} ${SVC_CFG_MERGED}

# Calculate predictable IP for this pod (if NETWORK_ATTACHMENT_DEFINITION is set)
if [[ -n "${NETWORK_ATTACHMENT_DEFINITION}" ]]; then
    echo "Calculating predictable IP for pod..."
    ${SCRIPTPATH}/set-predictable-ip.sh
    if [[ $? -eq 0 ]]; then
        # Source the predictable IP environment variables
        source /var/lib/config-data/merged/predictable-ip.env
        echo "Using predictable IP: ${PREDICTABLE_IP}"

        # Update designate.conf to listen on the specific IP (for mdns service)
        if [[ -f "/var/lib/config-data/merged/designate.conf" ]]; then
            sed -i "s/listen=0.0.0.0:5354/listen=${PREDICTABLE_IP}:5354/" /var/lib/config-data/merged/designate.conf
            echo "Updated designate.conf to listen on ${PREDICTABLE_IP}:5354"
        fi
    else
        echo "WARNING: Failed to calculate predictable IP, using default configuration"
    fi
else
    echo "INFO: NETWORK_ATTACHMENT_DEFINITION not set, using default configuration"
fi

# Merge all templates from core config secret
for dir in /var/lib/config-data/default; do
    merge_config_dir ${dir}
done

#  Merge all templates from service specific config secret
if test -d /var/lib/config-data/service; then
    for dir in /var/lib/config-data/service; do
        merge_config_dir ${dir}
    done
fi

# Handle any default overrides that might be mounted.
# First check that destinations exists!
OVERWRITE_DEST=/var/lib/config-data/config-overwrites
if test -d ${OVERWRITE_DEST}; then
    if test -d /var/lib/config-data/common-overwrites; then
        cp -a /var/lib/config-data/common-overwrites ${OVERWRITE_DEST}
    fi
    if test -d /var/lib/config-data/overwrites; then
        cp -a /var/lib/config-data/overwrites ${OVERWRITE_DEST}
    fi
fi

# Provide an empty custom.conf if none was created.
# Keeps kolla happy
if ! test -e /var/lib/config-data/merged/custom.conf; then
    echo "# Custom conf - see CustomServiceConfig" > /var/lib/config-data/merged/custom.conf
fi
