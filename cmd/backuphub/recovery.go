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
	if len(args) == 0 || (args[0] != "export" && args[0] != "restore" && args[0] != "reconcile") {
		return fmt.Errorf("usage: backuphub recovery {export|restore|reconcile}")
	}
	flags := flag.NewFlagSet("recovery "+args[0], flag.ContinueOnError)
	dataDir := flags.String("data-dir", "/var/lib/backuphub", "private state directory")
	var kit, repositoryID string
	if args[0] == "reconcile" {
		flags.StringVar(&repositoryID, "repository-id", "", "storage ID (default: all configured storages)")
	} else {
		flags.StringVar(&kit, "kit", "", "private recovery kit directory")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *dataDir == "" || (kit == "" && args[0] != "reconcile") {
		return fmt.Errorf("invalid recovery arguments; use backuphub recovery %s -h", args[0])
	}
	if args[0] == "restore" {
		if err := backup.RestoreRecoveryKit(ctx, kit, *dataDir); err != nil {
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
	if args[0] == "reconcile" {
		return reconcileRepositories(ctx, c, repositoryID)
	}
	if err = backup.ExportRecoveryKit(ctx, c, kit); err != nil {
		return err
	}
	fmt.Println("Private recovery kit saved to", kit)
	return nil
}

func reconcileRepositories(ctx context.Context, c *catalog.Catalog, repositoryID string) error {
	ids := []string{repositoryID}
	if repositoryID == "" {
		rows, err := c.DB.QueryContext(ctx, "SELECT id FROM storage_repositories WHERE path<>'' ORDER BY id")
		if err != nil {
			return err
		}
		defer rows.Close()
		ids = nil
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		if err = rows.Err(); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return fmt.Errorf("no repositories configured")
	}
	for _, id := range ids {
		added, err := backup.ReconcileRecoveryRepository(ctx, c, id)
		if err != nil {
			return fmt.Errorf("reconcile %s: %w", id, err)
		}
		fmt.Printf("Repository %s: imported %d snapshot(s)\n", id, added)
	}
	return nil
}

func openRecoveryCatalog(dataDir string) (*catalog.Catalog, error) {
	if _, err := os.Lstat(filepath.Join(dataDir, "catalog.db")); err != nil {
		return nil, fmt.Errorf("open existing recovery catalog: %w", err)
	}
	return catalog.Open(dataDir)
}
