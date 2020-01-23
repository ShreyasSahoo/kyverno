package policy

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"sync"

	openapi_v2 "github.com/googleapis/gnostic/OpenAPIv2"
	"github.com/googleapis/gnostic/compiler"
	"github.com/nirmata/kyverno/pkg/engine/utils"
	"k8s.io/kube-openapi/pkg/util/proto"
	"k8s.io/kube-openapi/pkg/util/proto/validation"

	"gopkg.in/yaml.v2"
)

var validationGlobalState struct {
	document    *openapi_v2.Document
	definitions map[string]*openapi_v2.Schema
	models      proto.Models
	isSet       bool
}

func ValidateMutationPatches(kind string, policyPatch []byte) error {
	kind = "io.k8s.api.core.v1." + kind

	if validationGlobalState.isSet == false {
		err := setValidationGlobalState()
		if err != nil {
			return err
		}
	}

	emptyResourceObject := generateEmptyResource(validationGlobalState.definitions[kind])
	emptyResourceObjectRaw, err := json.Marshal(emptyResourceObject)
	if err != nil {
		return err
	}

	patchedResource, err := utils.ApplyPatchNew(emptyResourceObjectRaw, policyPatch)
	if err != nil {
		return err
	}

	return validateResource(patchedResource, kind)
}

func setValidationGlobalState() error {
	var err error
	validationGlobalState.document, err = getSchemaDocument("./swagger.json")
	if err != nil {
		return err
	}

	validationGlobalState.definitions = make(map[string]*openapi_v2.Schema)

	for _, definition := range validationGlobalState.document.GetDefinitions().AdditionalProperties {
		validationGlobalState.definitions[definition.GetName()] = definition.GetValue()
	}

	validationGlobalState.models, err = proto.NewOpenAPIData(validationGlobalState.document)
	if err != nil {
		return err
	}

	validationGlobalState.isSet = true
	return nil
}

func getSchemaDocument(path string) (*openapi_v2.Document, error) {
	_, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	specRaw, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var spec yaml.MapSlice
	err = yaml.Unmarshal(specRaw, &spec)
	if err != nil {
		return nil, err
	}

	return openapi_v2.NewDocument(spec, compiler.NewContext("$root", nil))
}

func validateResource(patchedResourceRaw []byte, kind string) error {
	var patchedResource interface{}
	if err := json.Unmarshal(patchedResourceRaw, &patchedResource); err != nil {
		return fmt.Errorf("pre-validation: failed to parse yaml: %v", err)
	}

	schema := validationGlobalState.models.LookupModel(kind)
	if schema == nil {
		return fmt.Errorf("pre-validation: couldn't find model %s", kind)
	}

	if errs := validation.ValidateModel(patchedResource, schema, kind); len(errs) > 0 {
		var errorMessages []string
		for i := range errs {
			errorMessages = append(errorMessages, errs[i].Error())
		}

		return fmt.Errorf(strings.Join(errorMessages, "\n\n"))
	}

	return nil
}

func generateEmptyResource(kindSchema *openapi_v2.Schema) interface{} {

	types := kindSchema.GetType().GetValue()
	if len(types) != 1 {
		if kindSchema.GetXRef() != "" {
			return generateEmptyResource(validationGlobalState.definitions[strings.TrimPrefix(kindSchema.GetXRef(), "#/definitions/")])
		}
		properties := kindSchema.GetProperties().GetAdditionalProperties()
		if len(properties) == 0 {
			return nil
		}

		var props = make(map[string]interface{})
		var wg sync.WaitGroup
		var mutex sync.Mutex
		wg.Add(len(properties))
		for _, property := range properties {
			go func(property *openapi_v2.NamedSchema) {
				prop := generateEmptyResource(property.GetValue())
				mutex.Lock()
				props[property.GetName()] = prop
				mutex.Unlock()
				wg.Done()
			}(property)
		}
		wg.Wait()
		return props
	}

	switch types[0] {
	case "object":
		properties := kindSchema.GetProperties().GetAdditionalProperties()
		if len(properties) == 0 {
			return nil
		}

		var props = make(map[string]interface{})
		var wg sync.WaitGroup
		var mutex sync.Mutex
		wg.Add(len(properties))
		for _, property := range properties {
			go func(property *openapi_v2.NamedSchema) {
				prop := generateEmptyResource(property.GetValue())
				mutex.Lock()
				props[property.GetName()] = prop
				mutex.Unlock()
				wg.Done()
			}(property)
		}
		wg.Wait()
		return props
	case "array":
		var array []interface{}
		for _, schema := range kindSchema.GetItems().GetSchema() {
			array = append(array, generateEmptyResource(schema))
		}
		return array
	case "string":
		if kindSchema.GetDefault() != nil {
			return string(kindSchema.GetDefault().Value.Value)
		}
		if kindSchema.GetExample() != nil {
			return string(kindSchema.GetExample().GetValue().Value)
		}
		return ""
	case "integer":
		if kindSchema.GetDefault() != nil {
			val, _ := strconv.Atoi(string(kindSchema.GetDefault().Value.Value))
			return val
		}
		if kindSchema.GetExample() != nil {
			val, _ := strconv.Atoi(string(kindSchema.GetExample().GetValue().Value))
			return val
		}
		return 0
	case "number":
		if kindSchema.GetDefault() != nil {
			val, _ := strconv.Atoi(string(kindSchema.GetDefault().Value.Value))
			return val
		}
		if kindSchema.GetExample() != nil {
			val, _ := strconv.Atoi(string(kindSchema.GetExample().GetValue().Value))
			return val
		}
		return 0
	case "boolean":
		if kindSchema.GetDefault() != nil {
			if string(kindSchema.GetDefault().Value.Value) == "true" {
				return true
			}
			return false
		}
		if kindSchema.GetExample() != nil {
			if string(kindSchema.GetExample().GetValue().Value) == "true" {
				return true
			}
			return false
		}
		return false
	}

	return nil
}
