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

# This script reads the predictable IP from pod annotations that were set by the Pod annotation controller.
# The Pod annotation controller calculates the IP based on the pod index and network parameters.

if [[ -z "${POD_NAME}" ]]; then
    echo "ERROR: POD_NAME environment variable is required"
    exit 1
fi

# Extract pod index from pod name (format: {statefulset-name}-{ordinal})
POD_INDEX=$(echo "${POD_NAME}" | sed 's/.*-//')
if ! [[ "${POD_INDEX}" =~ ^[0-9]+$ ]]; then
    echo "ERROR: Could not extract pod index from pod name: ${POD_NAME}"
    exit 1
fi

echo "Pod name: ${POD_NAME}, Pod index: ${POD_INDEX}"

# Try to read the predictable IP from pod annotations
# The pod annotations are available through the downward API
if [[ -n "${PREDICTABLE_IP_FROM_ANNOTATION}" ]]; then
    PREDICTABLE_IP="${PREDICTABLE_IP_FROM_ANNOTATION}"
    echo "Using predictable IP from annotation: ${PREDICTABLE_IP}"
else
    echo "WARNING: PREDICTABLE_IP_FROM_ANNOTATION not set, using default IP calculation"
    echo "This may indicate that the Pod annotation controller has not yet processed this pod."
    
    # Fallback: try to read from a file that might have been created by the Pod annotation controller
    if [[ -f "/var/lib/config-data/merged/predictable-ip.env" ]]; then
        echo "Found existing predictable IP configuration file"
        source /var/lib/config-data/merged/predictable-ip.env
        if [[ -n "${PREDICTABLE_IP}" ]]; then
            echo "Using predictable IP from existing file: ${PREDICTABLE_IP}"
        else
            echo "ERROR: No predictable IP found in existing file"
            exit 1
        fi
    else
        echo "ERROR: No predictable IP available from annotations or existing file"
        echo "The Pod annotation controller should have set the predictable IP annotation."
        exit 1
    fi
fi

# Write the IP to a file that can be sourced by other scripts
echo "export PREDICTABLE_IP=${PREDICTABLE_IP}" > /var/lib/config-data/merged/predictable-ip.env
echo "export POD_INDEX=${POD_INDEX}" >> /var/lib/config-data/merged/predictable-ip.env

echo "Predictable IP configuration written to /var/lib/config-data/merged/predictable-ip.env"
