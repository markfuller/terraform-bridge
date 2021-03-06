package bridge

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/hashicorp/terraform/helper/schema"
)

var saltValue int
var prefixTemplateData prefixData
var code []string

type prefixData struct {
	Namespace   string
	StructTypes []string
}

type providerData struct {
	StructType          string
	NativeType          string
	ProvidedAttributes  []string
	ImmutableAttributes []string
}

type structData struct {
	StructType   string
	StructFields []structField
	InsertID     bool
}

type structField struct {
	FieldName string
	FieldType string
}

var prefixTemplate = `// Code generated by Lyra DO NOT EDIT.

// This code is generated on a per-provider basis using "tf-gen"
// Long term our hope is to remove this generation step and adopt dynamic approach

package generated

import (
	"sync"

	"github.com/davecgh/go-spew/spew"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/terraform"
	"github.com/lyraproj/terraform-bridge/pkg/bridge"
	"github.com/lyraproj/pcore/px"
	"github.com/lyraproj/servicesdk/service"
)

var once sync.Once
var Config *terraform.ResourceConfig

func configureProvider(p *schema.Provider) {
	once.Do(func() {
		if Config == nil {
			Config = &terraform.ResourceConfig{
				Config: map[string]interface{}{},
			}
		}
		err := p.Configure(Config)
		if err != nil {
			panic(err)
		}
	})
}

func Initialize(sb *service.Builder, p *schema.Provider) {
	var evs []px.Type
{{range .StructTypes}}
	evs = sb.RegisterTypes("{{$.Namespace}}", sb.BuildResource(&{{.}}{}, {{.}}_rtb))
	sb.RegisterHandler("{{$.Namespace}}::{{.}}Handler", &{{.}}Handler{provider: p}, evs[0])
{{end}}
}
`

var typeTemplate = `
type {{.StructType}} struct {
{{if .InsertID}}
	{{.StructType}}_id *string ` + "`" + `lyra:"ignore"` + "`" + `{{end}}
{{range .StructFields}}
    {{.FieldName}} {{.FieldType}}
{{end}}
}
`

var providerTemplate = `
var {{.StructType}}_rtb = func(rtb service.ResourceTypeBuilder) {
	rtb.ProvidedAttributes(
		"{{.NativeType}}_id",
{{range .ProvidedAttributes}}
		"{{.}}",
{{end}}
	)
	rtb.ImmutableAttributes(
{{range .ImmutableAttributes}}
		"{{.}}",
{{end}}
	)
}

// {{.StructType}}Handler ...
type {{.StructType}}Handler struct {
	provider *schema.Provider
}

// Create ...
func (h *{{.StructType}}Handler) Create(desired *{{.StructType}}) (*{{.StructType}}, string, error) {
	log := hclog.Default()
	if log.IsInfo() {
		log.Info("Create {{.StructType}}", "desired", spew.Sdump(desired))
	}
	configureProvider(h.provider)
	rc := &terraform.ResourceConfig{
		Config: bridge.TerraformMarshal(desired),
	}
	id, err := bridge.Create(h.provider, "{{.NativeType}}", rc)
	if err != nil {
		return nil, "", err
	}
	actual, err := h.Read(id)
	if err != nil {
		return nil, "", err
	}
	return actual, id, nil
}

// Update ...
func (h *{{.StructType}}Handler) Update(externalID string, desired *{{.StructType}}) (*{{.StructType}}, error) {
	log := hclog.Default()
	if log.IsInfo() {
		log.Info("Update {{.StructType}}", "desired", spew.Sdump(desired))
	}
	configureProvider(h.provider)
	rc := &terraform.ResourceConfig{
		Config: bridge.TerraformMarshal(desired),
	}
	actual, err := bridge.Update(h.provider, "{{.NativeType}}", externalID,  rc)
	if err != nil {
		return nil, err
	}
	x := &{{.StructType}}{ {{.StructType}}_id: &externalID }
	bridge.TerraformUnmarshal(actual, x)
	if log.IsInfo() {
		log.Info("Update Actual State {{.StructType}}", "actual", spew.Sdump(x))
	}
	return x, nil
}

// Read ...
func (h *{{.StructType}}Handler) Read(externalID string) (*{{.StructType}}, error) {
	log := hclog.Default()
	if log.IsInfo() {
		log.Info("Read {{.StructType}}", "externalID", externalID)
	}
	configureProvider(h.provider)
	id, actual, err := bridge.Read(h.provider, "{{.NativeType}}", externalID)
	if err != nil {
		return nil, err
	}
	x := &{{.StructType}}{ {{.StructType}}_id: &id }
	bridge.TerraformUnmarshal(actual, x)
	if log.IsInfo() {
		log.Info("Read Actual State {{.StructType}}", "actual", spew.Sdump(x))
	}
	return x, nil
}

// Delete ...
func (h *{{.StructType}}Handler) Delete(externalID string) error {
	log := hclog.Default()
	if log.IsInfo() {
		log.Info("Delete {{.StructType}}", "externalID", externalID)
	}
	configureProvider(h.provider)
	return bridge.Delete(h.provider, "{{.NativeType}}", externalID)
}
`

func mkdirs(filename string) {
	dirName := filepath.Dir(filename)
	if _, serr := os.Stat(dirName); serr != nil {
		merr := os.MkdirAll(dirName, os.ModePerm)
		if merr != nil {
			panic(merr)
		}
	}
}

func deriveGoType(goType, name string) string {
	saltValue++
	return fmt.Sprintf("%s_%s_%d", goType, name, saltValue)
}

func getGoType(goType, name string, s *schema.Schema) string {
	//   TypeBool - bool
	//   TypeInt - int
	//   TypeFloat - float64
	//   TypeString - string
	//   TypeList - []interface{}
	//   TypeMap - map[string]interface{}
	//   TypeSet - *schema.Set
	var t string
	switch s.Type {
	case schema.TypeBool:
		t = "bool"
	case schema.TypeInt:
		t = "int"
	case schema.TypeFloat:
		t = "float64"
	case schema.TypeString:
		t = "string"
	case schema.TypeList:
		switch s.Elem.(type) {
		case *schema.Resource:
			t = deriveGoType(goType, name)
			generateResourceType(t, s.Elem.(*schema.Resource), false)
			t = "[]" + t
		case *schema.Schema:
			t = "[]" + getGoType(goType, name, s.Elem.(*schema.Schema))
		default:
			panic(fmt.Sprintf("Unsupported TypeList: %v", s.Elem))
		}
	case schema.TypeMap:
		t = "map[string]string"
	case schema.TypeSet:
		switch s.Elem.(type) {
		case *schema.Resource:
			t = deriveGoType(goType, name)
			generateResourceType(t, s.Elem.(*schema.Resource), false)
			t = "[]" + t
		case *schema.Schema:
			t = "[]" + getGoType(goType, name, s.Elem.(*schema.Schema))
		default:
			panic(fmt.Sprintf("Unsupported TypeSet: %v", s.Elem))
		}
	default:
		panic(fmt.Sprintf("Unknown schema type: %v", s.Type))
	}
	return t
}

func getGoTypeWithPtr(goType, name string, s *schema.Schema) string {
	t := getGoType(goType, name, s)
	if !s.Required {
		t = "*" + t
	}
	return t
}

func generatePrefix() {
	tmpl := template.Must(template.New("prefixTemplate").Parse(prefixTemplate))
	var buf bytes.Buffer
	err := tmpl.Execute(&buf, prefixTemplateData)
	if err != nil {
		panic(err)
	}
	code = append([]string{buf.String()}, code...)
}

func generateResourceType(nativeType string, r *schema.Resource, insertID bool) ([]string, []string) {

	// Sort field names to give predictable code generation
	var names []string
	for name := range r.Schema {
		names = append(names, name)
	}
	sort.Strings(names)

	// Check for provided and immutable attributes
	var providedAttributes []string
	var immutableAttributes []string
	for _, name := range names {
		schema := r.Schema[name]
		if schema.ForceNew {
			immutableAttributes = append(immutableAttributes, name)
		}
		if !schema.Required {
			providedAttributes = append(providedAttributes, name)
		}
	}

	// Determine field names and types
	structType := strings.Title(nativeType)
	var structFields []structField
	for _, name := range names {
		structFields = append(structFields, structField{
			FieldName: strings.Title(name),
			FieldType: getGoTypeWithPtr(structType, name, r.Schema[name]),
		})
	}

	// Build template data
	templateData := structData{
		StructType:   structType,
		StructFields: structFields,
		InsertID:     insertID,
	}

	// Render template
	tmpl := template.Must(template.New("typeTemplate").Parse(typeTemplate))
	var buf bytes.Buffer
	err := tmpl.Execute(&buf, templateData)
	if err != nil {
		panic(err)
	}
	code = append(code, buf.String())
	return providedAttributes, immutableAttributes
}

func generateResource(nativeType string, r *schema.Resource) {

	providedAttributes, immutableAttributes := generateResourceType(nativeType, r, true)
	structType := strings.Title(nativeType)
	prefixTemplateData.StructTypes = append(prefixTemplateData.StructTypes, structType)
	templateData := providerData{
		StructType:          structType,
		NativeType:          nativeType,
		ProvidedAttributes:  providedAttributes,
		ImmutableAttributes: immutableAttributes,
	}

	// Render template
	tmpl := template.Must(template.New("providerTemplate").Parse(providerTemplate))
	var buf bytes.Buffer
	err := tmpl.Execute(&buf, templateData)
	if err != nil {
		panic(err)
	}

	code = append(code, buf.String())
}

// formatCode reformats the code as `go fmt` would
func formatCode() {
	for k, v := range code {
		b, err := format.Source([]byte(v))
		if err != nil {
			panic(fmt.Sprintf("Unexpected rror running format.Source on %v", v))
		}
		code[k] = string(b)
	}
}

func writeSourceFile(filename string) {
	mkdirs(filename)
	f, err := os.Create(filename)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	for _, s := range code {
		_, err = f.WriteString(s)
		if err != nil {
			panic(err)
		}
	}
}

// Generate the Lyra boilerplate needed to bridge to a Terraform provider
func Generate(p *schema.Provider, ns, filename string) {

	// Reset
	saltValue = 0
	prefixTemplateData = prefixData{Namespace: ns}
	code = []string{}

	// Sort native types to give predicatable output
	nativeTypes := make([]string, 0)
	for nativeType := range p.ResourcesMap {
		nativeTypes = append(nativeTypes, nativeType)
	}
	sort.Strings(nativeTypes)

	// Generate code
	for _, nativeType := range nativeTypes {
		r := p.ResourcesMap[nativeType]
		generateResource(nativeType, r)
	}
	generatePrefix()

	formatCode()

	// Write source
	writeSourceFile(filename)

}
