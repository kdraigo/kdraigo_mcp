package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/config"
	"github.com/kdraigo/kdraigo_mcp/internal/vault"
)

const vaultUsage = `kdraigo-mcp vault — a research lab as a folder of markdown files (Obsidian opens it).

  kdraigo-mcp vault pull --lab <slug> [--dir <path>]   the first time, into an empty folder
  kdraigo-mcp vault pull [--dir <path>]                later: the platform's changes
  kdraigo-mcp vault push [--dir <path>] [--delete]     your edits to Brief.md, Notes/ and Lessons/
  kdraigo-mcp vault status [--dir <path>]              what a push would send

--dir defaults to the current folder.`

// runVault runs a vault subcommand with the key in ~/.kdraigo/config.yaml.
func runVault(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(out, vaultUsage)
		return nil
	}
	cmd := args[0]
	flags := flag.NewFlagSet("vault "+cmd, flag.ContinueOnError)
	flags.SetOutput(out)
	dir := flags.String("dir", ".", "the vault `folder`")
	lab := flags.String("lab", "", "the lab's slug or id (pull, the first time)")
	del := flags.Bool("delete", false, "push: also delete on the platform the notes deleted here")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	signer, err := auth.NewSigner(cfg.Auth.KeyID, cfg.Auth.PrivateKey)
	if err != nil {
		return err
	}
	api := vault.NewAPI(client.NewHTTP(cfg.Endpoint, cfg.BacktesterEndpoint, signer))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	var rep *vault.Report
	switch cmd {
	case "pull":
		rep, err = vault.Pull(ctx, api, *dir, *lab, time.Now())
	case "push":
		rep, err = vault.Push(ctx, api, *dir, vault.PushOptions{Delete: *del})
	case "status":
		rep, err = vault.Push(ctx, api, *dir, vault.PushOptions{Delete: *del, DryRun: true})
	default:
		return errors.New("unknown vault command " + cmd + "\n\n" + vaultUsage)
	}
	if rep != nil {
		fmt.Fprint(out, rep.String())
	}
	return err
}
