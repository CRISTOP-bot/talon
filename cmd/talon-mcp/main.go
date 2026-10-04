// Command talon-mcp serves Talon's read-only project tools over the Model
// Context Protocol, so another agent can search and read a codebase through the
// same code paths Talon uses.
//
// Configure it in an MCP client like this:
//
//	{
//	  "mcpServers": {
//	    "talon": {
//	      "command": "talon-mcp",
//	      "args": ["--cwd", "/path/to/project"]
//	    }
//	  }
//	}
//
// Only read-only tools are exposed. Write and execute tools are refused even if
// a client asks for them by name, because a client that connects over MCP is not
// the person sitting at the terminal.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/CRISTOP-bot/talon/internal/audit"
	"github.com/CRISTOP-bot/talon/internal/config"
	"github.com/CRISTOP-bot/talon/internal/index"
	"github.com/CRISTOP-bot/talon/internal/mcp"
	"github.com/CRISTOP-bot/talon/internal/paths"
	"github.com/CRISTOP-bot/talon/internal/perm"
	"github.com/CRISTOP-bot/talon/internal/project"
	"github.com/CRISTOP-bot/talon/internal/tools"
)

func main() {
	var (
		dir      string
		verbose  bool
		auditLog bool
	)
	fs := flag.NewFlagSet("talon-mcp", flag.ContinueOnError)
	fs.StringVar(&dir, "cwd", "", "project directory (default: the working directory)")
	fs.BoolVar(&verbose, "v", false, "log to stderr")
	fs.BoolVar(&auditLog, "audit", false, "record tool decisions in the local audit log")
	fs.Parse(os.Args[1:])

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if dir != "" {
		root, err = filepath.Abs(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	if _, err := os.Stat(root); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// stdout carries the protocol: nothing else may be written there.
	if err := serve(ctx, root, verbose, auditLog); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, root string, verbose, auditLog bool) error {
	cfg, err := config.Load(root)
	if err != nil {
		// A malformed configuration must not stop a read-only server.
		cfg = config.Defaults()
	}
	limits := tools.DefaultLimits()
	limits.MaxOutputBytes = cfg.Agent.MaxToolOutput

	registry := tools.NewRegistry()
	registry.Register(tools.All()...)

	idx := index.New(root)
	if err := idx.Build(ctx); err != nil && verbose {
		fmt.Fprintf(os.Stderr, "talon-mcp: index: %v\n", err)
	}

	toolCtx := &tools.Context{
		Ctx:       ctx,
		Workspace: root,
		Limits:    limits,
		Policy:    perm.New(perm.Config{Level: perm.ReadOnly, Workspace: root}),
		Index:     idx,
		Project:   project.Detect(ctx, root),
	}
	if auditLog {
		log, aerr := audit.Open(audit.Options{
			Path:    filepath.Join(paths.Data(), "audit", "audit.jsonl"),
			Enabled: true, Level: audit.LevelNormal, SessionID: "mcp",
		})
		if aerr != nil {
			return fmt.Errorf("cannot open the audit log: %w", aerr)
		}
		defer log.Close()
		toolCtx.Gates.Audit = log
	}

	if verbose {
		readOnly := 0
		for _, def := range registry.All() {
			if def.Risk == perm.RiskRead {
				readOnly++
			}
		}
		fmt.Fprintf(os.Stderr, "talon-mcp: exposing %d read-only tool(s) for %s\n", readOnly, root)
	}
	return mcp.Serve(ctx, os.Stdin, os.Stdout, registry, toolCtx)
}
