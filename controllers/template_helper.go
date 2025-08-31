/*
Copyright 2025. projectsveltos.io. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/go-logr/logr"
	"gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/projectsveltos/libsveltos/lib/funcmap"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func getTemplateFuncMap(useTextTemplate bool, logger logr.Logger) template.FuncMap {
	templateFuncMap := funcmap.SveltosFuncMap(useTextTemplate)

	templateFuncMap["getResource"] = func(u map[string]interface{}) map[string]interface{} {
		uObject := resetFields(u)

		return uObject.Object
	}

	templateFuncMap["copy"] = func(u map[string]interface{}) string {
		uObject := resetFields(u)

		data, err := yaml.Marshal(uObject.UnstructuredContent())
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return ""
		}

		return strings.TrimSuffix(string(data), "\n")
	}
	templateFuncMap["getField"] = func(u map[string]interface{}, fields string) interface{} {
		v, isPresent, err := unstructured.NestedFieldCopy(u, strings.Split(fields, ".")...)
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return ""
		}

		if !isPresent {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("field %s does not exist", fields))
			return ""
		}

		return v
	}
	templateFuncMap["removeField"] = func(u map[string]interface{}, fields string) string {
		unstructured.RemoveNestedField(u, strings.Split(fields, ".")...)
		uObject := resetFields(u)

		data, err := yaml.Marshal(uObject.UnstructuredContent())
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return ""
		}

		return strings.TrimSuffix(string(data), "\n")
	}
	templateFuncMap["setField"] = func(u map[string]interface{}, fields string, value any) string {
		err := unstructured.SetNestedField(u, value, strings.Split(fields, ".")...)
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return ""
		}

		uObject := resetFields(u)

		data, err := yaml.Marshal(uObject.UnstructuredContent())
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return ""
		}

		return strings.TrimSuffix(string(data), "\n")
	}
	templateFuncMap["chainRemoveField"] = func(u map[string]interface{}, fields string) map[string]interface{} {
		unstructured.RemoveNestedField(u, strings.Split(fields, ".")...)

		return u
	}
	templateFuncMap["chainSetField"] = func(u map[string]interface{}, fields string, value any) map[string]interface{} {
		err := unstructured.SetNestedField(u, value, strings.Split(fields, ".")...)
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return nil
		}

		return u
	}

	return templateFuncMap
}

func resetFields(u map[string]interface{}) unstructured.Unstructured {
	var uObject unstructured.Unstructured
	uObject.SetUnstructuredContent(u)
	uObject.SetManagedFields(nil)
	uObject.SetResourceVersion("")
	uObject.SetUID("")

	return uObject
}
