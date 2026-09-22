package protocol

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type schemaField struct {
	typeName string
	repeated bool
}

var protoFieldPattern = regexp.MustCompile(`(?m)^\s*(repeated\s+)?([A-Za-z][A-Za-z0-9_]*)\s+([a-z][a-z0-9_]*)\s*=\s*[0-9]+;`)

func TestControlProtoMatchesJSONSchema(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	protoPath := filepath.Join(root, "proto", "control.proto")
	protoMessages := parseProtoMessages(t, protoPath)
	protoMessages["ControlMessage"] = parseProtoOneof(t, protoPath, "ControlMessage", "payload")
	goMessages := parseGoMessages(t, filepath.Join(root, "pkg", "protocol", "pb", "messages.go"))

	compareStringSets(t, "message type", keys(goMessages), keys(protoMessages))
	for messageName, goFields := range goMessages {
		protoFields, ok := protoMessages[messageName]
		if !ok {
			continue
		}
		compareFields(t, messageName, goFields, protoFields)
	}

	envelope := protoMessages["ControlMessage"]
	for field := range controlMessageFields {
		if _, ok := envelope[field]; !ok {
			t.Errorf("JSON control field %q is missing from proto/control.proto", field)
		}
	}
	for field := range envelope {
		if _, ok := controlMessageFields[field]; !ok {
			t.Errorf("proto/control.proto field %q is not accepted by the JSON control reader", field)
		}
	}
}

func TestProtoOneofParserExcludesOuterEnvelopeFields(t *testing.T) {
	t.Parallel()
	text := `message ControlMessage {
  oneof payload {
    Ping ping = 1;
  }
  Pong pong = 2;
}`
	fields, err := protoOneofFields(text, "ControlMessage", "payload")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["ping"]; !ok {
		t.Fatal("oneof parser omitted field inside payload")
	}
	if _, ok := fields["pong"]; ok {
		t.Fatal("oneof parser accepted field outside payload")
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("locate package working directory: %v", err)
	}
	return filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
}

func parseProtoMessages(t *testing.T, path string) map[string]map[string]schemaField {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // Test reads a fixed repository file.
	if err != nil {
		t.Fatalf("read control.proto: %v", err)
	}
	text := string(data)
	messagePattern := regexp.MustCompile(`(?m)^message\s+([A-Za-z][A-Za-z0-9_]*)\s*\{`)
	messages := make(map[string]map[string]schemaField)
	for _, match := range messagePattern.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		bodyStart := match[1]
		bodyEnd, err := matchingBrace(text, bodyStart-1)
		if err != nil {
			t.Fatalf("parse proto message %s: %v", name, err)
		}
		fields := make(map[string]schemaField)
		for _, fieldMatch := range protoFieldPattern.FindAllStringSubmatch(text[bodyStart:bodyEnd], -1) {
			fields[fieldMatch[3]] = schemaField{
				typeName: fieldMatch[2],
				repeated: fieldMatch[1] != "",
			}
		}
		messages[name] = fields
	}
	return messages
}

func parseProtoOneof(t *testing.T, path, messageName, oneofName string) map[string]schemaField {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // Test reads a fixed repository file.
	if err != nil {
		t.Fatalf("read control.proto: %v", err)
	}
	fields, err := protoOneofFields(string(data), messageName, oneofName)
	if err != nil {
		t.Fatal(err)
	}
	return fields
}

func protoOneofFields(text, messageName, oneofName string) (map[string]schemaField, error) {
	messagePattern := regexp.MustCompile(`(?m)^message\s+` + regexp.QuoteMeta(messageName) + `\s*\{`)
	messageMatch := messagePattern.FindStringIndex(text)
	if messageMatch == nil {
		return nil, fmt.Errorf("proto message %s not found", messageName)
	}
	messageEnd, err := matchingBrace(text, messageMatch[1]-1)
	if err != nil {
		return nil, fmt.Errorf("parse proto message %s: %w", messageName, err)
	}
	oneofPattern := regexp.MustCompile(`(?m)^\s*oneof\s+` + regexp.QuoteMeta(oneofName) + `\s*\{`)
	oneofMatch := oneofPattern.FindStringIndex(text[messageMatch[1]:messageEnd])
	if oneofMatch == nil {
		return nil, fmt.Errorf("proto message %s has no oneof %s", messageName, oneofName)
	}
	oneofOpen := messageMatch[1] + oneofMatch[1] - 1
	oneofEnd, err := matchingBrace(text, oneofOpen)
	if err != nil {
		return nil, fmt.Errorf("parse proto oneof %s.%s: %w", messageName, oneofName, err)
	}
	fields := make(map[string]schemaField)
	for _, fieldMatch := range protoFieldPattern.FindAllStringSubmatch(text[oneofOpen+1:oneofEnd], -1) {
		fields[fieldMatch[3]] = schemaField{
			typeName: fieldMatch[2],
			repeated: fieldMatch[1] != "",
		}
	}
	return fields, nil
}

func matchingBrace(text string, open int) (int, error) {
	depth := 0
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unclosed brace")
}

func parseGoMessages(t *testing.T, path string) map[string]map[string]schemaField {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse messages.go: %v", err)
	}
	messages := make(map[string]map[string]schemaField)
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			fields := make(map[string]schemaField)
			for _, field := range structure.Fields.List {
				if field.Tag == nil || len(field.Names) != 1 {
					continue
				}
				tag, err := strconvUnquote(field.Tag.Value)
				if err != nil {
					t.Fatalf("parse %s.%s struct tag: %v", typeSpec.Name.Name, field.Names[0].Name, err)
				}
				jsonName := strings.Split(reflect.StructTag(tag).Get("json"), ",")[0]
				if jsonName == "" || jsonName == "-" {
					continue
				}
				fieldType, repeated, err := goSchemaType(field.Type)
				if err != nil {
					t.Fatalf("map %s.%s type: %v", typeSpec.Name.Name, field.Names[0].Name, err)
				}
				fields[jsonName] = schemaField{typeName: fieldType, repeated: repeated}
			}
			messages[typeSpec.Name.Name] = fields
		}
	}
	return messages
}

func strconvUnquote(value string) (string, error) {
	if len(value) < 2 || value[0] != '`' || value[len(value)-1] != '`' {
		return "", fmt.Errorf("expected raw string tag, got %q", value)
	}
	return value[1 : len(value)-1], nil
}

func goSchemaType(expression ast.Expr) (string, bool, error) {
	switch value := expression.(type) {
	case *ast.StarExpr:
		name, repeated, err := goSchemaType(value.X)
		return name, repeated, err
	case *ast.ArrayType:
		if ident, ok := value.Elt.(*ast.Ident); ok && ident.Name == "byte" {
			return "bytes", false, nil
		}
		name, _, err := goSchemaType(value.Elt)
		return name, true, err
	case *ast.Ident:
		if value.Name == "int" {
			return "int32", false, nil
		}
		return value.Name, false, nil
	case *ast.SelectorExpr:
		if pkg, ok := value.X.(*ast.Ident); ok && pkg.Name == "time" && value.Sel.Name == "Time" {
			return "string", false, nil
		}
		return "", false, fmt.Errorf("unsupported selector expression")
	default:
		return "", false, fmt.Errorf("unsupported Go expression %T", expression)
	}
}

func compareFields(t *testing.T, message string, goFields, protoFields map[string]schemaField) {
	t.Helper()
	compareStringSets(t, message+" field", keys(goFields), keys(protoFields))
	for name, goField := range goFields {
		protoField, ok := protoFields[name]
		if !ok {
			continue
		}
		if goField != protoField {
			t.Errorf("%s.%s mismatch: Go JSON is %+v, proto is %+v", message, name, goField, protoField)
		}
	}
}

func compareStringSets(t *testing.T, label string, left, right []string) {
	t.Helper()
	leftSet := make(map[string]struct{}, len(left))
	rightSet := make(map[string]struct{}, len(right))
	for _, value := range left {
		leftSet[value] = struct{}{}
	}
	for _, value := range right {
		rightSet[value] = struct{}{}
	}
	for value := range leftSet {
		if _, ok := rightSet[value]; !ok {
			t.Errorf("Go JSON %s %q is missing from proto/control.proto", label, value)
		}
	}
	for value := range rightSet {
		if _, ok := leftSet[value]; !ok {
			t.Errorf("proto/control.proto %s %q is missing from Go JSON structs", label, value)
		}
	}
}

func keys[V any](values map[string]V) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
