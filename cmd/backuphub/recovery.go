package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mirivlad/BackupHub/internal/backup"
	"github.com/mirivlad/BackupHub/internal/catalog"
)

func runRecovery(ctx context.Context, args []string) error {
	if len(args) == 0 || (args[0] != "export" && args[0] != "restore") {
		return fmt.Errorf("usage: backuphub recovery {export|restore} -data-dir PATH -kit PATH")
	}
	flags := flag.NewFlagSet("recovery "+args[0], flag.ContinueOnError)
	dataDir := flags.String("data-dir", "/var/lib/backuphub", "private state directory")
	kit := flags.String("kit", "", "private recovery kit directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *kit == "" || *dataDir == "" {
		return fmt.Errorf("-kit and -data-dir must name directories")
	}
	if args[0] == "restore" {
		if err := backup.RestoreRecoveryKit(ctx, *kit, *dataDir); err != nil {
			return err
		}
		fmt.Println("Recovered catalog and keys in", *dataDir, "(schedules paused)")
		return nil
	}
	c, err := openRecoveryCatalog(*dataDir)
	if err != nil {
		return err
	}
	defer c.Close()
	if err = backup.ExportRecoveryKit(ctx, c, *kit); err != nil {
		return err
	}
	fmt.Println("Private recovery kit saved to", *kit)
	return nil
}

func openRecoveryCatalog(dataDir string) (*catalog.Catalog, error) {
	if _, err := os.Lstat(filepath.Join(dataDir, "catalog.db")); err != nil {
		return nil, fmt.Errorf("open existing recovery catalog: %w", err)
	}
	return catalog.Open(dataDir)
}
