package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func validateInput(in CreateSource, repo, state, stage string) error {
	if len(in.Password) > 4096 || len(in.Host) > 255 || len(in.Username) > 256 || len(in.Path) > 4096 || len(in.Share) > 255 || len(in.Database) > 256 {
		return errors.New("source setting too long")
	}
	switch in.Kind {
	case "local":
		if !filepath.IsAbs(in.Path) || filepath.Clean(in.Path) == "/" {
			return errors.New("local source must be an absolute non-root directory")
		}
		abs := filepath.Clean(in.Path)
		for _, protected := range []string{repo, state, stage} {
			if protected == "" {
				continue
			}
			p := filepath.Clean(protected)
			if abs == p || strings.HasPrefix(p, abs+string(os.PathSeparator)) || strings.HasPrefix(abs, p+string(os.PathSeparator)) {
				return errors.New("source overlaps BackupHub state/repository")
			}
		}
	case "smb":
		if in.Host == "" || in.Share == "" || in.Username == "" || in.Password == "" {
			return errors.New("SMB host/share/username/password required")
		}
		if strings.ContainsAny(in.Host, "/\\@\x00") || strings.ContainsAny(in.Share, "/\\\x00") {
			return errors.New("invalid SMB host or share")
		}
	case "sftp":
		if in.Host == "" || in.Username == "" || !strings.HasPrefix(in.Path, "/") {
			return errors.New("SFTP host, username and absolute path required")
		}
		if strings.ContainsAny(in.Host, "/@\\\x00") {
			return errors.New("invalid SFTP host")
		}
		if in.Password == "" {
			return errors.New("SFTP password required in current profile")
		}
	case "postgres":
		if in.Host == "" || in.Username == "" || in.Database == "" || in.Password == "" {
			return errors.New("PostgreSQL host/user/database/password required")
		}
		if strings.ContainsAny(in.Host, "/@\\\x00") || strings.ContainsAny(in.Database, "/\\\x00") {
			return errors.New("invalid PostgreSQL host/database")
		}
	}
	return nil
}
