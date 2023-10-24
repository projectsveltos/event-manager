#
#Copyright 2023. projectsveltos.io. All rights reserved.
#
#Licensed under the Apache License, Version 2.0 (the "License");
#you may not use this file except in compliance with the License.
#You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
#Unless required by applicable law or agreed to in writing, software
#distributed under the License is distributed on an "AS IS" BASIS,
#WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#See the License for the specific language governing permissions and
#limitations under the License.
#

#!/bin/bash

# Define the YAML file path
yaml_file=$1
output_file=$2

# Create a temporary directory to store the split sections
mkdir -p temp_yaml_sections

# Initialize variables for section counting and flag to track changes
section_count=0
in_section=false

# Iterate through the YAML file
while IFS= read -r line; do
    if [[ $line == '---' ]]; then
        # Start a new section
        in_section=true
        section_count=$((section_count + 1))
        current_section_file="temp_yaml_sections/section_${section_count}.yaml"
        continue
    fi

    if [[ $in_section == true ]]; then
        # Replace "shard-key=" with "shard-key='shard1'"
        if [[ $line == *"shard-key"* ]]; then
            line=$(echo "$line" | sed "s/shard-key=/shard-key={{.SHARD}}/")
        fi

        # Replace "name" to contain shard info
        if [[ $line == *"name: event-manager"* ]]; then
            line=$(echo "$line" | sed "s/event-manager/event-manager-{{.SHARD}}/")
        fi

        # Write the line to the current section file
        echo "$line" >> "$current_section_file"
    fi
done < "$yaml_file"

# Iterate through the split sections and print those with "kind: Deployment"
for section_file in temp_yaml_sections/*.yaml; do
    if grep -q "kind: Deployment" "$section_file"; then
        cat "$section_file" > $output_file
    fi
done

# Remove the temporary directory
rm -r temp_yaml_sections