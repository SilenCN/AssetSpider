package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

const internalPrefix = "github.com/SilenCN/AssetSpider/internal/"

// layerRules 是四层结构唯一的依赖方向约定：
//
//	router -> controller -> service -> repository
//
// 每层除「紧邻的下一层」外，只允许引用横切的 model / config / utils
// （领域模型、配置与工具类，不属于四层），以及本层自己的子包
// （如 service/collector）。任何跨层 import（例如 controller 直接用
// repository、router 直接用 service）都会让这个测试失败。
var layerRules = map[string]struct {
	allowed  map[string]bool
	requires string // 必须存在的紧邻下一层依赖
}{
	"router":     {allowed: map[string]bool{"controller": true, "utils": true}, requires: "controller"},
	"controller": {allowed: map[string]bool{"service": true, "utils": true}, requires: "service"},
	"service": {
		allowed:  map[string]bool{"repository": true, "model": true, "config": true, "utils": true},
		requires: "repository",
	},
	"repository": {allowed: map[string]bool{"model": true, "utils": true}, requires: "model"},
}

// productionImports 遍历某一层的生产代码（排除 _test.go），收集它 import 的
// internal 包以及全部第三方包。
func productionImports(t *testing.T, layer string) (map[string]bool, []string) {
	t.Helper()
	seenInternal := map[string]bool{}
	var all []string
	root := filepath.Join("internal", layer)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			all = append(all, p)
			if strings.HasPrefix(p, internalPrefix) {
				seenInternal[strings.Split(strings.TrimPrefix(p, internalPrefix), "/")[0]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 %s 失败: %v", root, err)
	}
	return seenInternal, all
}

func TestLayerDependencyDirection(t *testing.T) {
	for layer, rule := range layerRules {
		seen, _ := productionImports(t, layer)
		for target := range seen {
			if target == layer { // 本层子包，例如 service/collector
				continue
			}
			if !rule.allowed[target] {
				t.Errorf("分层违规：%s 层不允许直接依赖 internal/%s（只能走 router→controller→service→repository）", layer, target)
			}
		}
		if !seen[rule.requires] {
			t.Errorf("分层缺失：%s 层应当依赖 internal/%s", layer, rule.requires)
		}
	}
}

// TestRepositoryOneObjectPerEntity 固化仓储层约定：每个实体一个文件、
// 一个对外接口、一个实现对象、一个构造函数；容器 Repositories 不得夹带实体读写方法。
func TestRepositoryOneObjectPerEntity(t *testing.T) {
	entities := []struct {
		file  string
		iface string
		impl  string
		ctor  string
	}{
		{"asset.go", "AssetRepository", "assetRepository", "NewAssetRepository"},
		{"source.go", "SourceRepository", "sourceRepository", "NewSourceRepository"},
		{"video.go", "VideoRepository", "videoRepository", "NewVideoRepository"},
		{"category.go", "CategoryRepository", "categoryRepository", "NewCategoryRepository"},
		{"job.go", "JobRepository", "jobRepository", "NewJobRepository"},
		{"user.go", "UserRepository", "userRepository", "NewUserRepository"},
	}
	implNames := map[string]bool{}
	for _, e := range entities {
		implNames[e.impl] = true
	}
	parse := func(name string) *ast.File {
		t.Helper()
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("internal", "repository", name), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		return f
	}
	recvName := func(recv *ast.FieldList) string {
		if recv == nil || len(recv.List) != 1 {
			return ""
		}
		if star, ok := recv.List[0].Type.(*ast.StarExpr); ok {
			if id, ok := star.X.(*ast.Ident); ok {
				return id.Name
			}
		}
		return ""
	}

	for _, e := range entities {
		f := parse(e.file)
		ifaceCount, implCount, ctorCount, methodCount := 0, 0, 0, 0
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if ts.Name.Name == e.iface {
						if _, ok := ts.Type.(*ast.InterfaceType); ok {
							ifaceCount++
						}
					}
					if ts.Name.Name == e.impl {
						if _, ok := ts.Type.(*ast.StructType); ok {
							implCount++
						}
					}
				}
			case *ast.FuncDecl:
				if d.Name.Name == e.ctor {
					ctorCount++
				}
				switch recvName(d.Recv) {
				case e.impl:
					methodCount++
				case "":
					// 包级函数，不限制
				default:
					if implNames[recvName(d.Recv)] {
						t.Errorf("%s 只能定义 %s 自己的方法，却出现了 %s.%s",
							e.file, e.impl, recvName(d.Recv), d.Name.Name)
					}
				}
			}
		}
		if ifaceCount != 1 || implCount != 1 || ctorCount != 1 {
			t.Errorf("%s 应保持「1 个接口 + 1 个实现对象 + 1 个构造函数」，实际 %d/%d/%d",
				e.file, ifaceCount, implCount, ctorCount)
		}
		if methodCount == 0 {
			t.Errorf("%s 的实现对象 %s 没有任何方法", e.file, e.impl)
		}
	}

	// 扫描整个 repository 包：容器只做连接与装配，任何文件都不许往它身上挂实体方法。
	allowedContainerMethods := map[string]bool{"Path": true, "Close": true}
	files, err := filepath.Glob(filepath.Join("internal", "repository", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || recvName(fn.Recv) != "Repositories" {
				continue
			}
			if !allowedContainerMethods[fn.Name.Name] {
				t.Errorf("%s 给容器 Repositories 挂了实体方法 %s，请移到对应实体文件", path, fn.Name.Name)
			}
		}
	}
}

// TestUtilsIsCrossCutting 保证 utils 是纯工具层：不依赖任何业务包，
// 否则它会变成绕过四层调用的捷径。
func TestUtilsIsCrossCutting(t *testing.T) {
	_, imports := productionImports(t, "utils")
	for _, p := range imports {
		if strings.HasPrefix(p, internalPrefix) {
			t.Errorf("utils 不应依赖业务包（只能放无业务依赖的纯工具），实际 import %s", p)
		}
	}
}

// TestNoFrameworkLeak 防止 Web 框架与 ORM 越过各自所属的层。
func TestNoFrameworkLeak(t *testing.T) {
	checks := []struct {
		layer  string
		prefix string
	}{
		{"router", "gorm.io/"},
		{"controller", "gorm.io/"},
		{"service", "gorm.io/"},
		{"service", "github.com/gin-gonic/gin"},
		{"repository", "github.com/gin-gonic/gin"},
		{"repository", "github.com/SilenCN/AssetSpider/internal/service"},
		{"utils", "github.com/gin-gonic/gin"},
		{"utils", "gorm.io/"},
	}
	for _, c := range checks {
		_, imports := productionImports(t, c.layer)
		for _, p := range imports {
			if strings.HasPrefix(p, c.prefix) || p == c.prefix {
				t.Errorf("技术泄漏：%s 层不应 import %s（实际 %s）", c.layer, c.prefix, p)
			}
		}
	}
}
