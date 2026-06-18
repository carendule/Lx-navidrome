package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

const importedMediaDirName = "imports"

func (s *libraryService) ImportMediaFile(ctx context.Context, filePath string, libraryID ...int) (string, error) {
	if s == nil || s.ds == nil {
		return "", fmt.Errorf("library service is not configured")
	}
	if s.scanner == nil {
		return "", fmt.Errorf("scanner is not configured")
	}

	sourcePath, err := filepath.Abs(strings.TrimSpace(filePath))
	if err != nil {
		return "", err
	}
	sourcePath = filepath.Clean(sourcePath)

	libs, err := s.ds.Library(ctx).GetAll()
	if err != nil {
		return "", err
	}
	if len(libs) == 0 {
		return "", fmt.Errorf("no libraries are configured")
	}

	lib, err := s.selectImportLibrary(ctx, libs, sourcePath, libraryID...)
	if err != nil {
		return "", err
	}

	actualPath, relativeFilePath, scanFolderPath, err := prepareImportedFilePath(lib, sourcePath)
	if err != nil {
		return "", err
	}

	lookupPath := fmt.Sprintf("%d:%s", lib.ID, filepath.ToSlash(relativeFilePath))
	if mediaFiles, lookupErr := s.ds.MediaFile(ctx).FindByPaths([]string{lookupPath}); lookupErr != nil {
		return "", lookupErr
	} else if len(mediaFiles) > 0 {
		return mediaFiles[0].ID, nil
	}

	if actualPath != sourcePath {
		if copyErr := copyFile(sourcePath, actualPath); copyErr != nil {
			return "", copyErr
		}
	}

	if _, scanErr := s.scanner.ScanFolders(ctx, false, []model.ScanTarget{{LibraryID: lib.ID, FolderPath: scanFolderPath}}); scanErr != nil {
		return "", scanErr
	}

	mediaFiles, lookupErr := s.ds.MediaFile(ctx).FindByPaths([]string{lookupPath})
	if lookupErr != nil {
		return "", lookupErr
	}
	if len(mediaFiles) == 0 {
		return "", fmt.Errorf("imported media file not found after scan: %s", lookupPath)
	}

	return mediaFiles[0].ID, nil
}

func (s *libraryService) selectImportLibrary(ctx context.Context, libs model.Libraries, filePath string, libraryID ...int) (model.Library, error) {
	if len(libraryID) > 0 {
		for _, lib := range libs {
			if lib.ID == libraryID[0] {
				return lib, nil
			}
		}
		return model.Library{}, fmt.Errorf("library ID %d not found", libraryID[0])
	}

	sorted := append(model.Libraries(nil), libs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left := sorted[i].Path
		if abs, err := filepath.Abs(filepath.Clean(left)); err == nil {
			left = abs
		}
		right := sorted[j].Path
		if abs, err := filepath.Abs(filepath.Clean(right)); err == nil {
			right = abs
		}
		return len(filepath.Clean(left)) > len(filepath.Clean(right))
	})

	for _, lib := range sorted {
		if within, ok := pathWithinRoot(lib.Path, filePath); ok && within {
			return lib, nil
		}
	}

	if len(sorted) == 1 {
		return sorted[0], nil
	}

	log.Warn(ctx, "Import source is not inside any library, using the first available library",
		"filePath", filePath, "libraryID", sorted[0].ID, "libraryPath", sorted[0].Path)
	return sorted[0], nil
}

func prepareImportedFilePath(lib model.Library, sourcePath string) (string, string, string, error) {
	libraryRoot, err := filepath.Abs(filepath.Clean(lib.Path))
	if err != nil {
		return "", "", "", err
	}

	if within, ok := pathWithinRoot(libraryRoot, sourcePath); ok && within {
		relativeFilePath, err := filepath.Rel(filepath.Clean(libraryRoot), sourcePath)
		if err != nil {
			return "", "", "", err
		}
		relativeFilePath = filepath.Clean(relativeFilePath)
		return sourcePath, relativeFilePath, folderForRelativeFile(relativeFilePath), nil
	}

	importDir := filepath.Join(filepath.Clean(libraryRoot), importedMediaDirName)
	if err := os.MkdirAll(importDir, 0o755); err != nil {
		return "", "", "", err
	}

	actualPath := uniqueImportedMediaPath(importDir, filepath.Base(sourcePath))
	relativeFilePath, err := filepath.Rel(filepath.Clean(libraryRoot), actualPath)
	if err != nil {
		return "", "", "", err
	}
	relativeFilePath = filepath.Clean(relativeFilePath)
	return actualPath, relativeFilePath, folderForRelativeFile(relativeFilePath), nil
}

func folderForRelativeFile(relativeFilePath string) string {
	folder := filepath.Dir(relativeFilePath)
	if folder == "." {
		return ""
	}
	return folder
}

func pathWithinRoot(rootPath, candidatePath string) (bool, bool) {
	cleanRoot, err := filepath.Abs(filepath.Clean(rootPath))
	if err != nil {
		return false, false
	}
	cleanCandidate, err := filepath.Abs(filepath.Clean(candidatePath))
	if err != nil {
		return false, false
	}
	relativePath, err := filepath.Rel(cleanRoot, cleanCandidate)
	if err != nil {
		return false, false
	}
	if relativePath == "." {
		return true, true
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return false, true
	}
	return true, true
}

func uniqueImportedMediaPath(dir, fileName string) string {
	baseName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	ext := filepath.Ext(fileName)
	candidate := filepath.Join(dir, fileName)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}

	for idx := 1; ; idx++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", baseName, idx, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		_ = os.Remove(dstPath)
		return err
	}
	if err := dst.Sync(); err != nil {
		_ = os.Remove(dstPath)
		return err
	}
	return nil
}
