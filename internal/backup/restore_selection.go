package backup

import (
	"errors"
	"fmt"
	"hash/fnv"
	"path"
	"strings"
)

const restoreCheckSelectedMaxFiles = 256
const restoreCheckSelectedMaxBytes = 2 << 30

var ErrRestoreCheckPath = errors.New("нужен относительный путь к файлу или папке снимка")

func validRestoreCheckPath(rel string) bool {
	return rel != "" && !strings.HasPrefix(rel, "/") && path.Clean(rel) == rel &&
		rel != ".." && !strings.HasPrefix(rel, "../") && !strings.ContainsRune(rel, 0)
}

func planRestoreCheck(walk func(func(treeEntry) error) error, root, selection string) ([]treeEntry, int64, error) {
	var selected []treeEntry
	var size int64
	// A stable sample from two path-hash buckets in each of four size bands.
	// It keeps large and small files and ranges over the tree without an index.
	var bands [4][2]struct {
		score uint64
		entry treeEntry
	}
	var empty treeEntry
	var smallest uint64
	var lossyName bool
	found := selection == "."
	err := walk(func(entry treeEntry) error {
		rel, err := relativeTo(root, entry.Path)
		if err != nil || rel == "." || !validRestoreCheckPath(rel) {
			return nil
		}
		if strings.ContainsRune(rel, '\uFFFD') {
			if selection != "" && (rel == selection || selection == "." || strings.HasPrefix(rel, selection+"/")) {
				return errors.New("Restic вернул неоднозначное имя по выбранному пути")
			}
			lossyName = lossyName || entry.Type == "file"
			return nil
		}
		if selection != "" {
			match := rel == selection
			found = found || match && (entry.Type == "file" || entry.Type == "dir")
			if entry.Type != "file" || !(selection == "." || match || strings.HasPrefix(rel, selection+"/")) {
				return nil
			}
			if len(selected) == restoreCheckSelectedMaxFiles || entry.Size > uint64(restoreCheckSelectedMaxBytes-size) {
				return errors.New("выбранный путь превышает лимит пробного восстановления (256 файлов или 2 ГиБ), выберите меньшую папку или файл")
			}
			size += int64(entry.Size)
			selected = append(selected, entry)
			return nil
		}
		if entry.Type != "file" {
			return nil
		}
		if entry.Size == 0 {
			if empty.Path == "" {
				empty = entry
			}
			return nil
		}
		if smallest == 0 || entry.Size < smallest {
			smallest = entry.Size
		}
		if entry.Size > restoreCheckMaxBytes {
			return nil
		}
		band := 0
		for _, cut := range []uint64{1 << 20, 16 << 20, 64 << 20} {
			if entry.Size > cut {
				band++
			}
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(rel))
		score := h.Sum64()
		bucket := &bands[band][score%2]
		if bucket.entry.Path == "" || score < bucket.score {
			bucket.entry, bucket.score = entry, score
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if selection != "" {
		if !found || len(selected) == 0 {
			return nil, 0, errors.New("по выбранному пути нет обычных файлов в снимке")
		}
		return selected, size, nil
	}
	for _, band := range bands {
		for _, candidate := range band {
			if entry := candidate.entry; entry.Path != "" && entry.Size <= uint64(restoreCheckMaxBytes-size) {
				size += int64(entry.Size)
				selected = append(selected, entry)
			}
		}
	}
	if len(selected) > 0 {
		return selected, size, nil
	}
	if smallest > restoreCheckMaxBytes {
		return nil, 0, fmt.Errorf("автоматическая проба не выполнялась: самый маленький непустой файл (%d байт) превышает лимит выборки 256 МиБ. Выберите файл до 2 ГиБ вручную или восстановите его обычным способом", smallest)
	}
	if empty.Path != "" {
		return []treeEntry{empty}, 0, nil
	}
	if lossyName {
		return nil, 0, errors.New("автоматическая проба не выполнялась: Restic вернул неоднозначные имена файлов")
	}
	return nil, 0, errors.New("снимок не содержит обычных файлов для пробного восстановления")
}
