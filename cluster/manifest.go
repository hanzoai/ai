// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package cluster

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"gopkg.in/yaml.v3"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// Manifest renders an application's deployment manifest from its template and
// parameters. Callers set it on the record before saving; nothing here touches a
// cluster. This is the only kustomize caller in the tree.
func Manifest(application *object.Application, lang string) (string, error) {
	template, err := object.GetTemplate(util.GetIdFromOwnerAndName(application.Owner, application.Template))
	if err != nil {
		return "", err
	}
	if template == nil {
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:the record: %s does not exist"), application.Template))
	}
	manifest := template.Manifest
	if template.EnableBasicConfig {
		manifest, err = renderConfig(application, template)
		if err != nil {
			return "", err
		}
	}
	manifest, err = renderKustomize(manifest, application.Parameters, lang)
	if err != nil {
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to generate manifest: %v"), err))
	}
	return manifest, nil
}

// renderConfig renders the manifest from the application's basic configuration
// options through the template.
func renderConfig(a *object.Application, template *object.Template) (string, error) {
	options := make(map[string]interface{}, len(a.BasicConfigOptions))
	for _, option := range a.BasicConfigOptions {
		options[option.Parameter] = option.Setting
	}
	return template.Render(map[string]interface{}{
		"application": map[string]interface{}{
			"name":      metadataName(a.Name),
			"namespace": a.Namespace,
		},
		"options": options,
	})
}

func renderKustomize(baseManifest, parameters string, lang string) (string, error) {
	// If no parameters provided, return base manifest directly
	if parameters == "" {
		return baseManifest, nil
	}
	// Create in-memory filesystem
	fs := filesys.MakeFsInMemory()
	// Create root directory first
	if err := fs.Mkdir("."); err != nil {
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to create root directory: %v"), err))
	}
	// Split and write base resource files
	resourceFiles := []string{}
	baseFiles := strings.Split(baseManifest, "---")
	for i, fileContent := range baseFiles {
		trimmedContent := strings.TrimSpace(fileContent)
		if trimmedContent == "" {
			continue
		}
		fileName := fmt.Sprintf("resource-%d.yaml", i)
		if err := fs.WriteFile(fileName, []byte(trimmedContent)); err != nil {
			return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to write resource file: %v"), err))
		}
		resourceFiles = append(resourceFiles, fileName)
	}
	// Write patch file
	patchFileName := "patch.yaml"
	if err := fs.WriteFile(patchFileName, []byte(parameters)); err != nil {
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to write patch file: %v"), err))
	}
	// Create kustomization.yaml
	kustomization := types.Kustomization{
		TypeMeta: types.TypeMeta{
			APIVersion: types.KustomizationVersion,
			Kind:       types.KustomizationKind,
		},
		Resources: resourceFiles,
		Patches: []types.Patch{
			{Path: patchFileName},
		},
	}
	kustomizationYaml, err := yaml.Marshal(kustomization)
	if err != nil {
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to marshal kustomization: %v"), err))
	}
	err = fs.WriteFile("kustomization.yaml", kustomizationYaml)
	if err != nil {
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to write kustomization.yaml: %v"), err))
	}
	// Run Kustomize with correct path
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	resMap, err := k.Run(fs, ".")
	if err != nil {
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:kustomize run failed: %v"), err))
	}
	// Convert to final YAML
	finalManifestBytes, err := resMap.AsYaml()
	if err != nil {
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to convert result to yaml: %v"), err))
	}
	return string(finalManifestBytes), nil
}

func metadataName(name string) string {
	if len(name) == 0 {
		name = "default-name"
	}
	name = strings.ToLower(name)
	re := regexp.MustCompile(`[^a-z0-9.-]+`)
	name = re.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-.")
	if len(name) > 253 {
		name = name[:253]
	}
	return name
}
