// Command apigen generates the frontend wire-type declarations
// (uni_console/src/types/api/api.generated.d.ts) from the JSON tags of the Go
// response DTOs in internal/model/dto/response.
//
// Wire contract ground truth = the `json` struct tags; swagger.json cannot be
// used as a source because it only carries Go field names, not the tag names.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// fileNamespace maps response DTO source files to the frontend Api namespace
// they populate. A file absent here is a fatal error: extending the wire
// contract must be an explicit, reviewed decision.
var fileNamespace = map[string]string{
	"auth.go":            "Auth",
	"cache.go":           "Monitor",
	"config.go":          "Config",
	"dept.go":            "System",
	"device.go":          "Device",
	"device_overview.go": "Device",
	"dict.go":            "Dict",
	"dict_data.go":       "Dict",
	"dict_type.go":       "Dict",
	"file.go":            "File",
	"id_resp.go":         "",
	"job.go":             "Job",
	"login_log.go":       "Log",
	"menu.go":            "System",
	"notice.go":          "Notice",
	"operation_log.go":   "Log",
	"online.go":          "Monitor",
	"pprof.go":           "Monitor",
	"role.go":            "System",
	"server_monitor.go":  "Monitor",
	"sql_monitor.go":     "Monitor",
	"sql_stats.go":       "Monitor",
	"user.go":            "System",
}

// namespaceOrder fixes emission order for byte-stable output.
//
// 该切片是**穷举**的:genPackage 只遍历它来发射命名空间,fileNamespace 里登记了
// 却不在本切片里的命名空间会被**静默丢弃**(不报错、不产出),故新增命名空间必须
// 同时改两处。位置只影响发射顺序(进而影响生成物的字节形状),不影响类型。
var namespaceOrder = []string{"Auth", "Dict", "System", "Notice", "Config", "Job", "Log", "Monitor", "Device", "File"}

// primitives maps Go predeclared identifiers to TypeScript types.
var primitives = map[string]string{
	"string": "string", "bool": "boolean",
	"int": "number", "int8": "number", "int16": "number", "int32": "number", "int64": "number",
	"uint": "number", "uint8": "number", "uint16": "number", "uint32": "number", "uint64": "number",
	"float32": "number", "float64": "number",
	"byte": "number", "rune": "number",
	"any": "unknown", "interface{}": "unknown", "error": "string",
}

// selectorTypes maps pkg.Type selectors to TypeScript types.
var selectorTypes = map[string]string{
	"time.Time":            "string",
	"util.JSONTime":        "string",
	"util.JsonUint64":      "string",
	"util.JsonUint64Slice": "string[]",
}

type structDef struct {
	Name      string
	Namespace string
	File      string
	Fields    []fieldDef
}

type fieldDef struct {
	Name        string
	JSONName    string
	Omit        bool
	Stringified bool
	TypeExpr    ast.Expr
	Embed       bool
}

type registry struct {
	byName map[string]*structDef
}

func main() {
	dir := flag.String("dir", "internal/model/dto/response", "Go DTO directory to parse")
	out := flag.String("out", "", "output file (default: stdout)")
	permout := flag.String("permout", "", "output file for frontend permission constants")
	flag.Parse()

	src, err := genPackage(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apigen:", err)
		os.Exit(1)
	}
	if *out == "" {
		// 兼容旧用法:未指定 -out 时把 wire 类型打到 stdout。
		// 仅当也不写权限常量时才打印,避免混合输出。
		if *permout == "" {
			fmt.Print(src)
		}
	} else if err := os.WriteFile(*out, []byte(src), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "apigen:", err)
		os.Exit(1)
	}

	if *permout != "" {
		psrc, err := genPermissionTS()
		if err != nil {
			fmt.Fprintln(os.Stderr, "apigen:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*permout, []byte(psrc), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "apigen:", err)
			os.Exit(1)
		}
	}
}

// genPermissionTS parses internal/pkg/permission/permission.go and emits a
// TypeScript module of named string constants mirroring the permission codes.
// The ground truth stays the Go permission package; this file is regenerated
// (and CI-diffed) exactly like api.generated.d.ts, so frontend v-perm / v-auth
// can reference typed constants instead of raw 'system:xxx' literals.
func genPermissionTS() (string, error) {
	const path = "internal/pkg/permission/permission.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return "", err
	}

	type permConst struct{ Name, Value string }
	var perms []permConst
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			name := vs.Names[0].Name
			if !strings.HasPrefix(name, "Perm") {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				return "", err
			}
			perms = append(perms, permConst{Name: name, Value: val})
		}
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i].Name < perms[j].Name })

	var b strings.Builder
	b.WriteString("// AUTO-GENERATED by uni_core/tools/apigen from internal/pkg/permission.\n")
	b.WriteString("// DO NOT EDIT. Run `pnpm gen:api` to regenerate.\n\n")
	b.WriteString("/**\n * 权限码单一来源 —— 镜像 uni_core/internal/pkg/permission 常量。\n")
	b.WriteString(" * 模板用这些常量替代裸字符串,避免前后端权限码静默漂移。\n */\n")
	for _, p := range perms {
		fmt.Fprintf(&b, "export const %s = %s as const\n", p.Name, tsQuote(p.Value))
	}
	return b.String(), nil
}

// tsQuote 以单引号渲染 TS 字符串字面量(对齐仓库 prettier 的 singleQuote,
// 使生成物 prettier-clean,免于把 permission.ts 加入 .prettierignore)。
func tsQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
}

// genPackage parses dir and returns the full .d.ts source.
func genPackage(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), "_test.go") || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)

	reg := &registry{byName: map[string]*structDef{}}
	var defs []*structDef

	for _, name := range files {
		ns, ok := fileNamespace[name]
		if !ok {
			return "", fmt.Errorf("response file %q has no Api namespace mapping (update fileNamespace in tools/apigen/main.go; refusing silent guess)", name)
		}
		path := filepath.Join(dir, name)
		pkgDefs, err := parseFile(path, ns, name)
		if err != nil {
			return "", err
		}
		defs = append(defs, pkgDefs...)
	}
	for _, d := range defs {
		reg.byName[d.Name] = d
	}

	var b strings.Builder
	b.WriteString("// AUTO-GENERATED by uni_core/tools/apigen from internal/model/dto/response.\n")
	b.WriteString("// DO NOT EDIT. Run `pnpm gen:api` to regenerate.\n")
	b.WriteString("declare namespace Api {\n")

	// Global namespace (""): id_resp.go 等无前缀的顶层接口。
	for _, d := range defs {
		if d.Namespace == "" {
			if err := emitInterface(&b, d, reg, nil); err != nil {
				return "", err
			}
		}
	}

	firstNS := true
	for _, ns := range namespaceOrder {
		var owned []*structDef
		for _, d := range defs {
			if d.Namespace == ns {
				owned = append(owned, d)
			}
		}
		if len(owned) == 0 {
			continue
		}
		if !firstNS {
			b.WriteString("\n")
		}
		firstNS = false
		fmt.Fprintf(&b, "  namespace %s {\n", ns)
		for i, d := range owned {
			if i > 0 {
				b.WriteString("\n")
			}
			if err := emitInterface(&b, d, reg, nil); err != nil {
				return "", err
			}
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}

func parseFile(path, ns, base string) ([]*structDef, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var defs []*structDef
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			d := &structDef{Name: ts.Name.Name, Namespace: ns, File: base}
			for _, fld := range st.Fields.List {
				d.Fields = append(d.Fields, parseField(fld))
			}
			defs = append(defs, d)
		}
	}
	return defs, nil
}

func parseField(f *ast.Field) fieldDef {
	// Embedded field: no names, no json tag → inline its struct's fields.
	if len(f.Names) == 0 && f.Tag == nil {
		if id, ok := f.Type.(*ast.Ident); ok {
			return fieldDef{Embed: true, Name: id.Name, TypeExpr: f.Type}
		}
	}
	name := "field"
	if len(f.Names) > 0 {
		name = f.Names[0].Name
	}
	fd := fieldDef{Name: name, JSONName: name, TypeExpr: f.Type}
	if f.Tag != nil {
		lit := f.Tag.Value
		if len(lit) >= 2 && (lit[0] == '`' || lit[0] == '\'') {
			lit = lit[1 : len(lit)-1]
		}
		rawJSON := reflectStructTag(lit, "json")
		if tag := strings.SplitN(rawJSON, ",", 2)[0]; tag != "" && tag != "-" {
			fd.JSONName = tag
		} else if tag == "-" {
			fd.JSONName = ""
		}
		fd.Omit = strings.Contains(rawJSON, "omitempty")
		fd.Stringified = strings.Contains(rawJSON, "string")
	}
	return fd
}

// reflectStructTag returns the value of a tag key in a raw struct tag literal
// without importing reflect (keeps tool dependencies nil and behavior local).
func reflectStructTag(raw, key string) string {
	for raw != "" {
		i := 0
		for i < len(raw) && raw[i] == ' ' {
			i++
		}
		raw = raw[i:]
		if raw == "" {
			break
		}
		i = 0
		for i < len(raw) && raw[i] > ' ' && raw[i] != ':' && raw[i] != '"' && raw[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(raw) || raw[i] != ':' || raw[i+1] != '"' {
			break
		}
		name := raw[:i]
		raw = raw[i+1:]
		i = 1
		for i < len(raw) && raw[i] != '"' {
			if raw[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(raw) {
			break
		}
		if name == key {
			return raw[1:i]
		}
		raw = raw[i+1:]
	}
	return ""
}

// tsType converts a Go field type expression to TypeScript, qualifying
// package-local struct references with their Api namespace.
func tsType(e ast.Expr, reg *registry) (string, error) {
	switch t := e.(type) {
	case *ast.StarExpr:
		inner, err := tsType(t.X, reg)
		if err != nil {
			return "", err
		}
		return inner + " | null", nil
	case *ast.ArrayType:
		inner, err := tsType(t.Elt, reg)
		if err != nil {
			return "", err
		}
		// 并集元素需加括号:(T | null)[] 正确,T | null[] 会解析成 T | (null[])。
		if strings.Contains(inner, " | ") {
			inner = "(" + inner + ")"
		}
		return inner + "[]", nil
	case *ast.MapType:
		k, err := tsType(t.Key, reg)
		if err != nil {
			return "", err
		}
		v, err := tsType(t.Value, reg)
		if err != nil {
			return "", err
		}
		return "Record<" + k + ", " + v + ">", nil
	case *ast.Ident:
		if t.Name == "interface{}" {
			return "unknown", nil
		}
		if prim, ok := primitives[t.Name]; ok {
			return prim, nil
		}
		if def, ok := reg.byName[t.Name]; ok {
			return "Api." + def.Namespace + "." + def.Name, nil
		}
		return "", fmt.Errorf("unknown local type %q (not a primitive nor a response DTO)", t.Name)
	case *ast.InterfaceType:
		return "unknown", nil
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			k := id.Name + "." + t.Sel.Name
			if prim, ok := selectorTypes[k]; ok {
				return prim, nil
			}
			return "", fmt.Errorf("unknown selector type %q (add it to selectorTypes)", k)
		}
		return "", fmt.Errorf("unsupported selector expression for %s", t.Sel.Name)
	default:
		return "", fmt.Errorf("unsupported type expression %T", e)
	}
}

// emitInterface writes one interface plus its embedded fields (inlined).
// Type errors are fatal: generation must never emit a silently wrong contract.
func emitInterface(b *strings.Builder, d *structDef, reg *registry, seenEmbeds map[string]bool) error {
	seen := map[string]bool{d.Name: true}
	if seenEmbeds != nil {
		for k := range seenEmbeds {
			seen[k] = true
		}
	}
	b.WriteString("    interface " + d.Name + " {\n")
	for _, f := range d.Fields {
		if f.Embed {
			base, ok := reg.byName[f.Name]
			if !ok {
				return fmt.Errorf("%s embeds unknown DTO %q", d.Name, f.Name)
			}
			if err := emitEmbeddedFields(b, base, reg, seen); err != nil {
				return err
			}
			continue
		}
		if f.JSONName == "" {
			continue // json:"-"
		}
		ts, err := tsType(f.TypeExpr, reg)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", d.Name, f.Name, err)
		}
		if f.Stringified {
			ts = "string"
		}
		if f.Omit {
			b.WriteString("      " + f.JSONName + "?: " + ts + "\n")
		} else {
			b.WriteString("      " + f.JSONName + ": " + ts + "\n")
		}
	}
	b.WriteString("    }\n")
	return nil
}

func emitEmbeddedFields(b *strings.Builder, d *structDef, reg *registry, seen map[string]bool) error {
	if seen[d.Name] {
		return nil // cycle guard: self-embedding terminates
	}
	seen[d.Name] = true
	for _, f := range d.Fields {
		if f.Embed {
			base, ok := reg.byName[f.Name]
			if !ok {
				return fmt.Errorf("embed %q resolves to unknown DTO", f.Name)
			}
			if err := emitEmbeddedFields(b, base, reg, seen); err != nil {
				return err
			}
			continue
		}
		if f.JSONName == "" {
			continue
		}
		ts, err := tsType(f.TypeExpr, reg)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", d.Name, f.Name, err)
		}
		if f.Stringified {
			ts = "string"
		}
		if f.Omit {
			b.WriteString("      " + f.JSONName + "?: " + ts + "\n")
		} else {
			b.WriteString("      " + f.JSONName + ": " + ts + "\n")
		}
	}
	return nil
}
