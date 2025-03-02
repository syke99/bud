package generator

import (
	_ "embed"
	"fmt"

	"github.com/livebud/bud/framework"
	"github.com/livebud/bud/internal/gobuild"

	"github.com/livebud/bud/package/di"
	"github.com/livebud/bud/package/log"
	"github.com/livebud/bud/package/parser"
	"github.com/livebud/bud/package/remotefs"

	"github.com/livebud/bud/internal/gotemplate"
	"github.com/livebud/bud/package/budfs"
	"github.com/livebud/bud/package/gomod"
)

//go:embed generator.gotext
var template string

var generator = gotemplate.MustParse("framework/generator/generator.gotext", template)

func Generate(state *State) ([]byte, error) {
	return generator.Generate(state)
}

func New(bfs *budfs.FileSystem, flag *framework.Flag, injector *di.Injector, log log.Log, module *gomod.Module, parser *parser.Parser) *Generator {
	return &Generator{bfs, flag, injector, log, module, parser, nil}
}

type Generator struct {
	bfs      *budfs.FileSystem
	flag     *framework.Flag
	injector *di.Injector
	log      log.Log
	module   *gomod.Module
	parser   *parser.Parser

	// process starts as nil
	process *remotefs.Process
}

// GenerateFile connects to the remotefs and mounts the remote directory.
func (g *Generator) GenerateFile(fsys budfs.FS, file *budfs.File) error {
	g.log.Debug("framework/generator: generating the main.go service containing the generators")
	state, err := Load(fsys, g.injector, g.log, g.module, g.parser)
	if err != nil {
		return fmt.Errorf("framework/generator: unable to load. %w", err)
	}
	code, err := Generate(state)
	if err != nil {
		return err
	}
	file.Data = code

	g.log.Debug("framework/generator: write the generator main.go file to bud/command/.generate/main.go")
	if err := g.module.MkdirAll("bud/command/.generate", 0755); err != nil {
		return err
	}
	if err := g.module.WriteFile("bud/command/.generate/main.go", []byte(code), 0644); err != nil {
		return err
	}

	g.log.Debug("framework/generator: build the main.go file to bud/.generate")
	builder := gobuild.New(g.module)
	builder.Env = append([]string{}, g.flag.Env...)
	builder.Stderr = g.flag.Stderr
	builder.Stdout = g.flag.Stdout
	if err := builder.Build(fsys.Context(), "bud/command/.generate/main.go", "bud/.generate"); err != nil {
		return fmt.Errorf("framework/generator: unable to build 'bud/.generate'. %s", err)
	}

	if g.process != nil {
		g.log.Debug("framework/generator: closing existing process")
		if err := g.process.Close(); err != nil {
			return err
		}
		g.process = nil
	}

	g.log.Debug("framework/generator: start bud/.generate that will serve the remote filesystem")
	cmd := &remotefs.Command{
		Dir:    g.module.Directory(),
		Env:    append([]string{}, g.flag.Env...),
		Stderr: g.flag.Stderr,
		Stdout: g.flag.Stdout,
	}
	g.process, err = cmd.Start(fsys.Context(), g.module.Directory("bud/.generate"))
	if err != nil {
		return err
	}

	// Close the process when the filesystem is closed.
	fsys.Defer(func() error {
		if g.process == nil {
			return nil
		}
		g.log.Debug("framework/generator: shutting down the remotefs")
		return g.process.Close()
	})

	// Mount the remote filesystem
	g.log.Debug("framework/generator: mounting the running remote filesystem")
	g.bfs.Mount(g.process)

	return nil
}
