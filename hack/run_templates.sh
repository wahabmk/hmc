#!/bin/sh
# Copyright 2024
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

set -eu

echo_cmd() {
  echo "$@"
  "$@"
}

KUBECTL=${KUBECTL:-kubectl}
NAMESPACE=${NAMESPACE:-hmc-system}

# Output directory for the generated Template manifests
TEMPLATES_OUTPUT_DIR=${TEMPLATES_OUTPUT_DIR:-templates/hmc-templates/files/templates}

for templ in $TEMPLATES_OUTPUT_DIR/*; do
  ns=$(cat $templ | grep '^  namespace:' | awk '{print $2}')
  if [[ -z $ns ]]; then
    echo_cmd $KUBECTL -n $NAMESPACE apply -f $templ
  else
    if out=$(KUBECTL create ns $ns 2>&1 > /dev/null); then
      echo "successfully created namespace '$ns' within template '$(basename $templ)'"
      echo_cmd $KUBECTL apply -f $templ
    else
      if [[ $out == *'(AlreadyExists)'* ]]; then
        echo "The namespace '$ns' within template '$(basename $templ)' already exists so applying..."
        echo_cmd $KUBECTL apply -f $templ
      else
        echo $out
        exit 1
      fi
    fi
  fi
done