[![CI](https://github.com/projectsveltos/event-manager/actions/workflows/main.yaml/badge.svg)](https://github.com/projectsveltos/event-manager/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/projectsveltos/event-manager)](https://goreportcard.com/report/github.com/projectsveltos/event-manager)
[![Slack](https://img.shields.io/badge/join%20slack-%23projectsveltos-brighteen)](https://join.slack.com/t/projectsveltos/shared_invite/zt-1hraownbr-W8NTs6LTimxLPB8Erj8Q6Q)
[![License](https://img.shields.io/badge/license-Apache-blue.svg)](LICENSE)
[![Twitter Follow](https://img.shields.io/twitter/follow/projectsveltos?style=social)](https://twitter.com/projectsveltos)

# Sveltos

<img src="https://raw.githubusercontent.com/projectsveltos/sveltos/main/docs/assets/logo.png" width="200">

Please refere to sveltos [documentation](https://projectsveltos.github.io/sveltos/).

## Event driven framework in action

Sveltos supports an event-driven add-on deployment oworkflow:

1. define what an event is;
2. select on which clusters;
3. define which add-ons to deploy when event happens.

[EventSource](https://github.com/projectsveltos/libsveltos/blob/main/api/v1beta1/eventsource_type.go) is the CRD introduced to define an event.

Sveltos supports custom events written in [Lua](https://www.lua.org/).

Following EventSource instance define an __event__ as a creation/deletion of a Service with label *sveltos: fv*.

```yaml
apiVersion: lib.projectsveltos.io/v1beta1
kind: EventSource
metadata:
 name: sveltos-service
spec:
 collectResources: true
 group: ""
 version: "v1"
 kind: "Service"
 labelsFilters:
 - key: sveltos
   operation: Equal
   value: fv
```

Sveltos supports custom events written in [Lua](https://www.lua.org/). 
Following EventSource instance again defines an Event as the creation/deletion of a Service with label *sveltos: fv* but using a Lua script. 

```yaml
apiVersion: lib.projectsveltos.io/v1beta1
kind: EventSource
metadata:
 name: sveltos-service
spec:
 collectResources: true
 group: ""
 version: "v1"
 kind: "Service"
 script: |
  function evaluate()
    hs = {}
    hs.matching = false
    hs.message = ""
    if obj.metadata.labels ~= nil then
      for key, value in pairs(obj.metadata.labels) do
        if key == "sveltos" then
          if value == "fv" then
            hs.matching = true
          end
        end
      end
    end
    return hs
  end
```

[EventTrigger](https://github.com/projectsveltos/libsveltos/blob/main/api/v1beta1/eventtrigger_type.go) is the CRD introduced to define what add-ons to deploy when an event happens.

![Sveltos Event Driven Framework](https://github.com/projectsveltos/demos/blob/main//event-driven/event_driven_framework.gif)

Event manager is a Sveltos micro service in charge of deploying add-ons when certain events happen in managed clusters.

## Contributing 

❤️ Your contributions are always welcome! If you want to contribute, have questions, noticed any bug or want to get the latest project news, you can connect with us in the following ways:

1. Open a bug/feature enhancement on github [![contributions welcome](https://img.shields.io/badge/contributions-welcome-brightgreen.svg?style=flat)](https://github.com/projectsveltos/addon-controller/issues)
2. Chat with us on the Slack in the #projectsveltos channel [![Slack](https://img.shields.io/badge/join%20slack-%23projectsveltos-brighteen)](https://join.slack.com/t/projectsveltos/shared_invite/zt-1hraownbr-W8NTs6LTimxLPB8Erj8Q6Q)
3. [Contact Us](mailto:support@projectsveltos.io)

## License

Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
