package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

type Provider struct {
	Name          string
	Source        string
	Constraint    string
	LockedVersion string
	Declared      bool
	Configured    bool
	Inferred      bool
	ResourceCount int
}

type Result struct {
	Root            string
	TerraformFiles  []string
	RequiredVersion string
	Providers       []Provider
}

type lockedProvider struct {
	Source  string
	Version string
}

func Scan(root string) (Result, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Result{}, fmt.Errorf("resolve project path: %w", err)
	}

	info, err := os.Stat(absRoot)

	if err != nil {
		return Result{}, fmt.Errorf("read project path: %w", err)
	}

	if !info.IsDir() {
		return Result{}, fmt.Errorf("project path is not a directory: %s", absRoot)
	}

	result := Result{
		Root: absRoot,
	}

	providers := make(map[string]Provider)

	err = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if path != absRoot && shouldSkipDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		if filepath.Ext(entry.Name()) != ".tf" {
			return nil
		}

		relativePath, err := filepath.Rel(absRoot, path)

		if err != nil {
			return err
		}

		result.TerraformFiles = append(result.TerraformFiles, relativePath)

		err = scanTerraformFile(path, &result, providers)

		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return Result{}, fmt.Errorf("scan Terraform files: %w", err)
	}

	lockedProvider, err := scanLockFile(filepath.Join(absRoot, ".terraform.lock.hcl"))

	mergeLockedProviders(providers, lockedProvider)

	if err != nil {
		return Result{}, err
	}

	for _, provider := range providers {
		result.Providers = append(result.Providers, provider)
	}
	sort.Slice(result.Providers, func(i, j int) bool {
		return result.Providers[i].Name < result.Providers[j].Name
	})
	sort.Strings(result.TerraformFiles)

	return result, nil
}

func shouldSkipDirectory(name string) bool {
	switch name {
	case ".git", ".terraform", ".idea", ".vscode", "node_modules", "testdata":
		return true
	default:
		return false
	}
}

func scanTerraformFile(path string, result *Result, providers map[string]Provider) error {

	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCLFile(path)
	if diagnostics.HasErrors() {
		return fmt.Errorf("parse %s: %s", path, diagnostics.Error())
	}

	body, ok := file.Body.(*hclsyntax.Body)

	if !ok {
		return fmt.Errorf("parse %s: unsupported HCL body", path)
	}

	for _, block := range body.Blocks {
		switch block.Type {
		case "terraform":
			scanTerraformBlock(
				block,
				result,
				providers,
			)
		case "provider":
			scanProviderBlock(
				block,
				providers,
			)
		case "resource", "data":
			scanResourceBlock(
				block,
				providers,
			)
		}
	}

	return nil
}

func scanTerraformBlock(block *hclsyntax.Block, result *Result, providers map[string]Provider) {
	if attribute, exists := block.Body.Attributes["required_version"]; exists && result.RequiredVersion == "" {
		if value, ok := staticString(attribute.Expr); ok {
			result.RequiredVersion = value
		}
	}

	for _, nestedBlock := range block.Body.Blocks {
		if nestedBlock.Type != "required_providers" {
			continue
		}

		for name, attribute := range nestedBlock.Body.Attributes {
			decoded, ok := decodeProvider(name, attribute.Expr)

			if !ok {
				continue
			}

			provider := getProvider(providers, name)

			provider.Declared = true

			if decoded.Source != "" {
				provider.Source = decoded.Source
			}

			if decoded.Constraint != "" {
				provider.Constraint = decoded.Constraint
			}

			providers[name] = provider
		}

	}
}

func scanProviderBlock(block *hclsyntax.Block, providers map[string]Provider) {
	if len(block.Labels) == 0 {
		return
	}

	name := block.Labels[0]

	provider := getProvider(providers, name)

	provider.Configured = true

	providers[name] = provider
}

func scanResourceBlock(block *hclsyntax.Block, providers map[string]Provider) {
	if len(block.Labels) == 0 {
		return
	}

	providerName := explicitProviderName(block)

	if providerName == "" {
		resourceType := block.Labels[0]
		providerName = providerNameFromResourceType(resourceType)
	}

	if providerName == "" {
		return
	}

	provider := getProvider(providers, providerName)

	provider.Inferred = true
	provider.ResourceCount++

	providers[providerName] = provider
}

// Se encarga de extraer el provider segun si existe en sus atributos un campo llamado provider
func explicitProviderName(block *hclsyntax.Block) string {
	attribute, exists := block.Body.Attributes["provider"]

	if !exists {
		return ""
	}

	traversal, diagnostics := hcl.AbsTraversalForExpr(attribute.Expr) //Busca la raiz

	if diagnostics.HasErrors() {
		return ""
	}

	return traversal.RootName()
}

// Se encarga de extraer el provider segun si existe en la denominacion del bloque, por ejemplo, aws_instance
func providerNameFromResourceType(resourceType string) string {

	position := strings.IndexByte(resourceType, '_')
	if position <= 0 {
		return ""
	}

	return resourceType[:position]
}

func decodeProvider(name string, expression hcl.Expression) (Provider, bool) {
	value, diagnostics := expression.Value(nil)
	if diagnostics.HasErrors() || !value.IsKnown() || value.IsNull() {
		return Provider{}, false
	}

	provider := Provider{Name: name}
	if value.Type() == cty.String {
		provider.Constraint = value.AsString()
		return provider, true
	}
	if !value.Type().IsObjectType() && !value.Type().IsMapType() {
		return Provider{}, false
	}

	values := value.AsValueMap()
	if source, ok := values["source"]; ok && source.IsKnown() && !source.IsNull() && source.Type() == cty.String {
		provider.Source = source.AsString()
	}
	if version, ok := values["version"]; ok && version.IsKnown() && !version.IsNull() && version.Type() == cty.String {
		provider.Constraint = version.AsString()
	}
	return provider, true
}

func scanLockFile(path string) ([]lockedProvider, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf(
			"read lockfile: %w",
			err,
		)
	}

	parser := hclparse.NewParser()

	file, diagnostics := parser.ParseHCLFile(path)

	if diagnostics.HasErrors() {
		return nil, fmt.Errorf(
			"parse lockfile: %s",
			diagnostics.Error(),
		)
	}

	body, ok := file.Body.(*hclsyntax.Body)

	if !ok {
		return nil, fmt.Errorf(
			"parse lockfile: unsupported HCL body",
		)
	}

	var locked []lockedProvider

	for _, block := range body.Blocks {
		if block.Type != "provider" || len(block.Labels) != 1 {
			continue
		}
		versionAttribute, exists := block.Body.Attributes["version"]

		if !exists {
			continue
		}

		version, ok := staticString(versionAttribute.Expr)

		if !ok {
			continue
		}

		locked = append(locked, lockedProvider{
			Source:  normalizeProviderSource(block.Labels[0]),
			Version: version,
		})
	}

	return locked, nil
}

func staticString(expression hcl.Expression) (string, bool) {
	value, diagnostics := expression.Value(nil)
	if diagnostics.HasErrors() || !value.IsKnown() || value.IsNull() || value.Type() != cty.String {
		return "", false
	}
	return value.AsString(), true
}

func mergeLockedProviders(providers map[string]Provider, lockedProviders []lockedProvider) {
	for _, locked := range lockedProviders {
		matchedName := ""
		for name, provider := range providers {
			if provider.Source != "" && normalizeProviderSource(provider.Source) == locked.Source {
				matchedName = name
				break
			}
		}

		if matchedName == "" {
			candidateName := providerNameFromSource(locked.Source)

			if _, exists := providers[candidateName]; exists {
				matchedName = candidateName
			}
		}

		if matchedName == "" {
			continue
		}

		provider := getProvider(providers, matchedName)

		if provider.Source == "" {
			provider.Source = locked.Source
		}

		provider.LockedVersion = locked.Version
		providers[matchedName] = provider
	}
}

func providerNameFromSource(source string) string {
	source = normalizeProviderSource(source)

	position := strings.LastIndexByte(source, '/')

	if position == -1 {
		return source
	}

	return source[position+1:]
}

func getProvider(providers map[string]Provider, name string) Provider {
	provider, exists := providers[name]
	if exists {
		return provider
	}
	return Provider{
		Name: name,
	}
}

func normalizeProviderSource(source string) string {
	return strings.TrimPrefix(source, "registry.terraform.io/")
}

type osDirFS struct{}

func (osDirFS) Open(name string) (fs.File, error) {
	return nil, fmt.Errorf("not implemented: %s", name)
}

func ReadLockedVersion(root string) (map[string]string, error) {
	absoluteRoot, err := filepath.Abs(root)

	if err != nil {
		return nil, fmt.Errorf("resolve project path: %w", err)
	}

	lockedProviders, err := scanLockFile(filepath.Join(absoluteRoot, ".terraform.lock.hcl"))

	if err != nil {
		return nil, err
	}

	versions := make(map[string]string)

	for _, provider := range lockedProviders {
		versions[provider.Source] = provider.Version
	}

	return versions, nil
}
