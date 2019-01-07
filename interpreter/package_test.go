package interpreter_test

import (
	"testing"

	"github.com/influxdata/flux/ast"

	"github.com/google/go-cmp/cmp"
	"github.com/influxdata/flux/interpreter"
	"github.com/influxdata/flux/parser"
	"github.com/influxdata/flux/semantic"
	"github.com/influxdata/flux/values"
)

// Implementation of interpreter.Importer
type importer struct {
	packages map[string]interpreter.Package
}

func (imp *importer) Import(path string) (semantic.PackageType, bool) {
	pkg, ok := imp.packages[path]
	if !ok {
		return semantic.PackageType{}, false
	}
	return semantic.PackageType{
		Name: pkg.Name(),
		Type: pkg.PolyType(),
	}, true
}

func (imp *importer) ImportPackageObject(path string) (interpreter.Package, bool) {
	pkg, ok := imp.packages[path]
	return pkg, ok
}

func TestInterpreter_EvalPackage(t *testing.T) {
	testcases := []struct {
		name        string
		imports     [](map[string]string)
		pkg         string
		want        values.Object
		sideEffects []values.Value
	}{
		{
			name: "simple",
			pkg: `
				package foo
				a = 1
				b = 2.0
				1 + 1
`,
			want: values.NewObjectWithValues(
				map[string]values.Value{
					"a": values.NewInt(1),
					"b": values.NewFloat(2.0),
				}),
		},
		{
			name: "import",
			imports: []map[string]string{
				{
					"path/to/bar": `
						package bar
						x = 10
`,
				},
			},
			pkg: ` 
				package foo
				import baz "path/to/bar"
				a = baz.x
`,
			want: values.NewObjectWithValues(
				map[string]values.Value{
					"a": values.NewInt(10),
				}),
		},
		{
			name: "nested variables",
			imports: []map[string]string{
				{
					"path/to/bar": `
						package bar
						f = () => {
							a = 2
							b = 3
							return a + b
						}
`,
				},
			},
			pkg: ` 
				package foo
				import "path/to/bar"
				a = bar.f()
`,
			want: values.NewObjectWithValues(
				map[string]values.Value{
					"a": values.NewInt(5),
				}),
		},
		{
			name: "polymorphic function",
			imports: []map[string]string{
				{
					"path/to/bar": `
						package bar
						f = (x) => x
`,
				},
			},
			pkg: `
				package foo
				import baz "path/to/bar"
				a = baz.f(x: 10)
`,
			want: values.NewObjectWithValues(
				map[string]values.Value{
					"a": values.NewInt(10),
				}),
		},
		{
			name: "multiple imports",
			imports: []map[string]string{
				{
					"path/to/a": `
						package a
						f = (x) => x
`,
				},
				{
					"path/to/b": `
						package b
						f = (x) => x + "ing"
`,
				},
			},
			pkg: `
				package foo
				import "path/to/a"
				import "path/to/b"

				x = a.f(x: 10)
				y = b.f(x: "str")
`,
			want: values.NewObjectWithValues(
				map[string]values.Value{
					"x": values.NewInt(10),
					"y": values.NewString("string"),
				}),
		},
		{
			name: "nested imports",
			imports: []map[string]string{
				{
					"path/to/a": `
						package a
						f = (x) => x
`,
				},
				{
					"path/to/b": `
						package b
						f = (x) => x + "ing"
`,
				},
				{
					"path/to/c": `
						package c
						import "path/to/a"
						import "path/to/b"
						x = a.f(x: 10)
						y = b.f(x: "str")
`,
				},
			},
			pkg: `
				package foo
				import "path/to/c"

				x = c.x + 10
				y = c.y + "s"
`,
			want: values.NewObjectWithValues(
				map[string]values.Value{
					"x": values.NewInt(20),
					"y": values.NewString("strings"),
				}),
		},
		{
			name: "main package",
			pkg: `
				package main
				x = 10
`,
			want: values.NewObjectWithValues(
				map[string]values.Value{
					"x": values.NewInt(10),
				}),
			sideEffects: []values.Value{
				values.NewInt(10),
			},
		},
		{
			name: "side effect",
			pkg: `
				package foo
				sideEffect()
`,
			sideEffects: []values.Value{
				values.NewInt(0),
			},
		},
		{
			name: "implicit main",
			pkg:  `x = 10`,
			want: values.NewObjectWithValues(
				map[string]values.Value{
					"x": values.NewInt(10),
				}),
			sideEffects: []values.Value{
				values.NewInt(10),
			},
		},
		{
			name: "explicit side effect",
			pkg: `
				package foo
				sideEffect()
`,
			sideEffects: []values.Value{
				values.NewInt(0),
			},
		},
		{
			name: "import side effect",
			imports: []map[string]string{
				{
					"path/to/foo": `
						package foo
						sideEffect()
`,
				},
			},
			pkg: `
				package main
				import "path/to/foo"
				x = 10
`,
			want: values.NewObjectWithValues(
				map[string]values.Value{
					"x": values.NewInt(10),
				}),
			sideEffects: []values.Value{
				values.NewInt(0), // side effect from `sideEffect()`
				values.NewInt(10),
			},
		},
	}
	builtins := map[string]values.Value{"sideEffect": &function{
		name: "sideEffect",
		t: semantic.NewFunctionPolyType(semantic.FunctionPolySignature{
			Required: nil,
			Return:   semantic.Int,
		}),
		call: func(args values.Object) (values.Value, error) {
			return values.NewInt(0), nil
		},
		hasSideEffect: true,
	}}
	for _, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			importer := &importer{
				packages: make(map[string]interpreter.Package),
			}
			for _, imp := range tc.imports {
				var path, pkg string
				for k, v := range imp {
					path = k
					pkg = v
				}
				itrp := interpreter.NewInterpreter(nil, builtins, nil)
				if err := eval(itrp, importer, pkg); err != nil {
					t.Fatal(err)
				}
				importer.packages[path] = itrp.Package()
			}
			itrp := interpreter.NewInterpreter(nil, builtins, nil)
			if err := eval(itrp, importer, tc.pkg); err != nil {
				t.Fatal(err)
			}
			got := itrp.Package()
			if tc.want != nil && !got.Equal(tc.want) {
				t.Errorf("unexpected package object -want/+got\n%s", cmp.Diff(tc.want, got))
			}
			sideEffects := got.SideEffects()
			if tc.sideEffects != nil && !cmp.Equal(tc.sideEffects, sideEffects) {
				t.Errorf("unexpected side effects -want/+got\n%s", cmp.Diff(tc.sideEffects, sideEffects))
			}
		})
	}
}

func eval(itrp *interpreter.Interpreter, importer interpreter.Importer, src string) error {
	pkg := parser.ParseSource(src)
	if ast.Check(pkg) > 0 {
		return ast.GetError(pkg)
	}
	node, err := semantic.New(pkg)
	if err != nil {
		return err
	}
	return itrp.Eval(node, importer)
}