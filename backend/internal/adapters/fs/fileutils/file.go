package fileutils

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"syscall"

	"github.com/gtsteffaniak/go-logger/logger"
)

var PermFile os.FileMode
var PermDir os.FileMode

// SetFsPermissions sets create modes from Unix chmod(2)-style values (for example
// values produced by strconv.ParseUint(s, 8, 32)). setuid, setgid, and sticky
// bits must be converted for Go's os.FileMode; a plain cast from Unix octal is
// incorrect for those bits.
func SetFsPermissions(unixFileMode, unixDirMode uint32) {
	PermFile = unixModeToFileMode(unixFileMode)
	PermDir = unixModeToFileMode(unixDirMode)
}

// EffectiveDirPerm returns [PermDir] if [SetFsPermissions] was called, otherwise 0o755. On Unix,
// [os.Mkdir] with perm 0 fails with permission denied, so use this (or [PermFile]) for creates when
// the process has not set globals yet.
func EffectiveDirPerm() os.FileMode {
	if PermDir == 0 {
		return 0o755
	}
	return PermDir
}

// EffectiveFilePerm returns [PermFile] if set, otherwise 0o644.
func EffectiveFilePerm() os.FileMode {
	if PermFile == 0 {
		return 0o644
	}
	return PermFile
}

func unixModeToFileMode(u uint32) os.FileMode {
	m := os.FileMode(u & 0777)
	if u&0o4000 != 0 {
		m |= os.ModeSetuid
	}
	if u&0o2000 != 0 {
		m |= os.ModeSetgid
	}
	if u&0o1000 != 0 {
		m |= os.ModeSticky
	}
	return m
}

// MoveFile moves a file from src to dst.
// By default, the rename system call is used. If src and dst are on different volumes,
// CopyFile is used as a fallback and the source is removed synchronously on success.
func MoveFile(src, dst string) error {
	return MoveFileHook(src, dst, nil)
}

// MoveFileHook behaves like MoveFile but reports progress through hook. When the
// move requires a cross-device copy fallback, byte-level progress is reported;
// for a plain rename the hook is notified once with the full size so callers
// can account for the item.
func MoveFileHook(src, dst string, hook *CopyHook) error {
	if err := os.Rename(src, dst); err != nil {
		if !isCrossDeviceRenameError(err) {
			return err
		}
		if err := CopyFileHook(src, dst, hook); err != nil {
			return err
		}
		return os.RemoveAll(src)
	}
	if hook != nil {
		if info, err := os.Lstat(src); err == nil && !info.IsDir() && info.Mode().IsRegular() {
			if !hook.AddBytes(info.Size()) {
				return ErrCopyCancelled
			}
		}
		hook.ItemDone()
	}
	return nil
}

func preserveFileMode(path string, mode os.FileMode) error {
	return os.Chmod(path, mode.Perm())
}

// CopyFile copies a file or directory from source to dest and returns an error if any.
func CopyFile(source, dest string) error {
	return CopyFileHook(source, dest, nil)
}

// CopyFileHook copies a file or directory from source to dest, reporting byte and
// item progress through hook when provided.
func CopyFileHook(source, dest string, hook *CopyHook) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return copySymlink(source, dest)
	}
	if info.IsDir() {
		return copyDirectoryHook(source, dest, hook)
	}
	return copySingleFile(source, dest, hook)
}

// copySymlink source to dest.
func copySymlink(source, dest string) error {
	target, err := os.Readlink(source)
	if err != nil {
		return err
	}
	destDir := filepath.Dir(dest)
	if err = os.MkdirAll(destDir, EffectiveDirPerm()); err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp(destDir, ".fb-link-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tmpPath := filepath.Join(tmpDir, "link")
	if err := os.Symlink(target, tmpPath); err != nil {
		return err
	}
	return os.Rename(tmpPath, dest)
}

// copySingleFile handles copying a single file.
func copySingleFile(source, dest string, hook *CopyHook) error {
	// Get source file info to preserve permissions
	srcInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	sourcePerms := srcInfo.Mode().Perm()

	// Open the source file.
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()

	// Create the destination directory if needed.
	err = os.MkdirAll(filepath.Dir(dest), EffectiveDirPerm())
	if err != nil {
		return err
	}

	// Create the destination file with source permissions
	dst, err := os.OpenFile(dest, os.O_RDWR|os.O_CREATE|os.O_TRUNC, sourcePerms)
	if err != nil {
		return err
	}
	defer dst.Close()

	// Copy the contents of the file.
	if err = copyFileContents(dst, src, hook); err != nil {
		// When cancelled mid-copy, remove the partial destination file so no
		// truncated copy is left behind.
		if errors.Is(err, ErrCopyCancelled) {
			_ = dst.Close()
			_ = os.Remove(dest)
		}
		return err
	}

	// Preserve source file permissions
	// Handle chmod errors gracefully (e.g., in rootless containers where chmod may be restricted)
	err = os.Chmod(dest, sourcePerms)
	if err != nil {
		// Log but don't fail - chmod may be restricted in some environments
		// The file was copied successfully, so we continue
		logger.Debugf("Could not set file permissions for %s (this may be expected in restricted environments): %v", dest, err)
	}
	// Preserve source file mod time
	if err := os.Chtimes(dest, srcInfo.ModTime(), srcInfo.ModTime()); err != nil {
		logger.Debugf("Could not preserve modification time for %s: %v", dest, err)
	}
	return nil
}

// copyFileContents copies the contents of src to dst.
func copyFileContents(dst io.Writer, src io.Reader, hook *CopyHook) error {
	if hook != nil {
		return copyFileContentsChunked(dst, src, hook)
	}
	_, err := io.Copy(dst, src)
	if !errors.Is(err, syscall.EBADF) {
		return err
	}

	// Some FUSE filesystems reject copy_file_range. Hide the optional fast-path
	// methods so io.Copy retries from the current offsets with buffered I/O.
	_, err = io.Copy(struct{ io.Writer }{dst}, struct{ io.Reader }{src})
	return err
}

// copyFileContentsChunked copies in fixed-size chunks so byte-level progress can
// be reported and cancellation can be observed mid-file.
func copyFileContentsChunked(dst io.Writer, src io.Reader, hook *CopyHook) error {
	buf := make([]byte, copyChunkSize)
	for {
		nr, rerr := src.Read(buf)
		if nr > 0 {
			nw, werr := dst.Write(buf[0:nr])
			if nw > 0 {
				if !hook.AddBytes(int64(nw)) {
					return ErrCopyCancelled
				}
			}
			if werr != nil {
				return werr
			}
			if nr != nw {
				return io.ErrShortWrite
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
	}
}

// copyDirectory handles copying directories recursively.
func copyDirectory(source, dest string) error {
	return copyDirectoryHook(source, dest, nil)
}

// copyDirectoryHook copies directories recursively, reporting progress through hook.
func copyDirectoryHook(source, dest string, hook *CopyHook) error {
	srcInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	// Create the destination directory.
	err = os.MkdirAll(dest, EffectiveDirPerm())
	if err != nil {
		return err
	}
	if err = preserveFileMode(dest, srcInfo.Mode()); err != nil {
		logger.Debugf("Could not set directory permissions for %s: %v", dest, err)
	}

	// Read the contents of the source directory.
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}

	// Iterate over each entry in the directory.
	for _, entry := range entries {
		if hook != nil && hook.IsCancelled() {
			return ErrCopyCancelled
		}
		srcPath := filepath.Join(source, entry.Name())
		destPath := filepath.Join(dest, entry.Name())

		if entry.Type()&os.ModeSymlink != 0 {
			linkTarget := ""
			linkTarget, err = os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err = os.Symlink(linkTarget, destPath); err != nil {
				return err
			}
			continue
		}
		if entry.IsDir() {
			// Recursively copy subdirectories.
			err = copyDirectoryHook(srcPath, destPath, hook)
			if err != nil {
				return err
			}
		} else {
			// Copy files.
			err = copySingleFile(srcPath, destPath, hook)
			if err != nil {
				return err
			}
			hook.ItemDone()
		}
	}
	return os.Chtimes(dest, srcInfo.ModTime(), srcInfo.ModTime())
}

// PreserveModTimes copies the mod times from src onto dst, this is used by the webdav COPY handler
func PreserveModTimes(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Chtimes(dst, info.ModTime(), info.ModTime())
	}
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			logger.Debugf("Error accessing %s to set mod time: %v", p, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			logger.Debugf("Error getting info for %s: %v", p, err)
			return nil
		}
		if err := os.Chtimes(filepath.Join(dst, rel), info.ModTime(), info.ModTime()); err != nil {
			logger.Debugf("Could not preserve modification time for %s: %v", filepath.Join(dst, rel), err)
		}
		return nil
	})
}

// CommonPrefix returns the common directory path of provided files.
func CommonPrefix(sep byte, paths ...string) string {
	// Handle special cases.
	switch len(paths) {
	case 0:
		return ""
	case 1:
		return path.Clean(paths[0])
	}

	// Treat string as []byte, not []rune as is often done in Go.
	c := []byte(path.Clean(paths[0]))

	// Add a trailing sep to handle the case where the common prefix directory
	// is included in the path list.
	c = append(c, sep)

	// Ignore the first path since it's already in c.
	for _, v := range paths[1:] {
		// Clean up each path before testing it.
		v = path.Clean(v) + string(sep)

		// Find the first non-common byte and truncate c.
		if len(v) < len(c) {
			c = c[:len(v)]
		}
		for i := 0; i < len(c); i++ {
			if v[i] != c[i] {
				c = c[:i]
				break
			}
		}
	}

	// Remove trailing non-separator characters and the final separator.
	for i := len(c) - 1; i >= 0; i-- {
		if c[i] == sep {
			c = c[:i]
			break
		}
	}

	return string(c)
}

func ClearCacheDir(cacheDir string) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		logger.Errorf("failed clear cache dir: %v", err)
	}

	for _, entry := range entries {
		path := filepath.Join(cacheDir, entry.Name())
		err = os.RemoveAll(path)
		if err != nil {
			logger.Errorf("failed clear cache dir: %v", err)
		}
	}

}

// ClearDirectoryContents removes all files and subdirectories inside dir; dir itself remains.
// If dir does not exist, returns nil.
func ClearDirectoryContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

// CopyContents copies src to dst. When hook is non-nil it copies in chunks so
// byte-level progress is reported and cancellation is honored mid-file
// (ErrCopyCancelled). With a nil hook it behaves like io.Copy.
func CopyContents(dst io.Writer, src io.Reader, hook *CopyHook) error {
	return copyFileContents(dst, src, hook)
}
