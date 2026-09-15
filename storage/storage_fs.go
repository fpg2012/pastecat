// Copyright (c) 2014-2015, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package storage

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type FileStore struct {
	sync.RWMutex
	cache map[ID]fileCache
	dir   string
}

type fileCache struct {
	path    string
	modTime time.Time
	size    int64
	reading sync.WaitGroup
}

type FilePaste struct {
	file  *os.File
	cache *fileCache
}

func (c FilePaste) Read(p []byte) (n int, err error) {
	return c.file.Read(p)
}

func (c FilePaste) ReadAt(p []byte, off int64) (n int, err error) {
	return c.file.ReadAt(p, off)
}

func (c FilePaste) Seek(offset int64, whence int) (int64, error) {
	return c.file.Seek(offset, whence)
}

func (c FilePaste) Close() error {
	err := c.file.Close()
	c.cache.reading.Done()
	return err
}

func (c FilePaste) ModTime() time.Time { return c.cache.modTime }

func (c FilePaste) Size() int64 { return c.cache.size }

func NewFileStore(stats *Stats, lifeTime time.Duration, dir string) (*FileStore, error) {
	if err := setupTopDir(dir); err != nil {
		return nil, err
	}
	s := new(FileStore)
	s.dir = dir
	s.cache = make(map[ID]fileCache)

	insert := func(id ID, path string, modTime time.Time, size int64) error {
		cached := fileCache{
			path:    path,
			size:    size,
			modTime: modTime,
		}
		s.cache[id] = cached
		return nil
	}
	if err := setupSubdirs(s.dir, fileRecover(insert, s, stats, lifeTime)); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileStore) Get(id ID) (Paste, error) {
	s.RLock()
	defer s.RUnlock()
	cached, e := s.cache[id]
	if !e {
		return nil, ErrPasteNotFound
	}
	f, err := os.Open(cached.path)
	if err != nil {
		return nil, err
	}
	cached.reading.Add(1)
	return FilePaste{file: f, cache: &cached}, nil
}

func writeNewFile(filename string, data []byte) error {
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if err == nil && n < len(data) {
		err = io.ErrShortWrite
	}
	if err1 := f.Close(); err == nil {
		err = err1
	}
	return err
}

func (s *FileStore) Put(content []byte) (ID, error) {
	size := int64(len(content))
	available := func(id ID) bool {
		_, e := s.cache[id]
		return !e
	}
	s.Lock()
	defer s.Unlock()
	id, err := randomID(available)
	if err != nil {
		return id, err
	}
	pastePath := pathFromID(id)
	if err = writeNewFile(pastePath, content); err != nil {
		return id, err
	}
	s.cache[id] = fileCache{
		path:    pastePath,
		size:    size,
		modTime: time.Now(),
	}
	return id, nil
}

func (s *FileStore) PutWithID(id ID, content []byte) error {
	s.Lock()
	defer s.Unlock()
	pastePath := pathFromID(id)
	if err := writeNewFile(pastePath, content); err != nil {
		if !os.IsExist(err) {
			return err
		}
		if err := os.Remove(pastePath); err != nil {
			return err
		}
		if err := writeNewFile(pastePath, content); err != nil {
			return err
		}
	}
	s.cache[id] = fileCache{
		path:    pastePath,
		size:    int64(len(content)),
		modTime: time.Now(),
	}
	return nil
}

func (s *FileStore) Delete(id ID) error {
	s.Lock()
	defer s.Unlock()
	cached, e := s.cache[id]
	if !e {
		return ErrPasteNotFound
	}
	cached.reading.Wait()
	if err := os.Remove(cached.path); err != nil {
		return err
	}
	delete(s.cache, id)
	return nil
}

// namesDir holds pastes whose id is not one of the original random eight
// character hexadecimal ids. Their file name is the hex encoding of the id.
const namesDir = "names"

// isHexID reports whether id is one of the original random eight character
// hexadecimal ids.
func isHexID(id ID) bool {
	s := string(id)
	if len(s) != idSize {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func pathFromID(id ID) string {
	if isHexID(id) {
		s := id.String()
		return filepath.Join(s[:2], s[2:])
	}
	return filepath.Join(namesDir, hex.EncodeToString([]byte(id)))
}

func idFromPath(path string) (ID, error) {
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid number of directories at %s", path)
	}
	if parts[0] == namesDir {
		b, err := hex.DecodeString(parts[1])
		if err != nil {
			return "", fmt.Errorf("invalid name file at %s", path)
		}
		return NewID(string(b))
	}
	if len(parts[0]) != 2 {
		return "", fmt.Errorf("invalid directory name length at %s", path)
	}
	hexID := parts[0] + parts[1]
	return IDFromString(hexID)
}

type fileInsert func(id ID, path string, modTime time.Time, size int64) error

func fileRecover(insert fileInsert, s Store, stats *Stats, lifeTime time.Duration) filepath.WalkFunc {
	startTime := time.Now()
	return func(path string, fileInfo os.FileInfo, err error) error {
		if err != nil || fileInfo.IsDir() {
			return err
		}
		id, err := idFromPath(path)
		if err != nil {
			return err
		}
		modTime := fileInfo.ModTime()
		lifeLeft := modTime.Add(lifeTime).Sub(startTime)
		if lifeTime > 0 && lifeLeft <= 0 {
			return os.Remove(path)
		}
		size := fileInfo.Size()
		if size == 0 {
			return os.Remove(path)
		}
		if err := stats.MakeSpaceFor(size); err != nil {
			return err
		}
		if err := insert(id, path, modTime, size); err != nil {
			return err
		}
		SetupPasteDeletion(s, stats, id, size, lifeLeft)
		return nil
	}
}

func setupTopDir(topdir string) error {
	if err := os.MkdirAll(topdir, 0700); err != nil {
		return err
	}
	return os.Chdir(topdir)
}

func setupSubdirs(topdir string, rec filepath.WalkFunc) error {
	for i := 0; i < 256; i++ {
		if err := setupSubdir(topdir, rec, byte(i)); err != nil {
			return err
		}
	}
	return setupNamesDir(topdir, rec)
}

func setupNamesDir(topdir string, rec filepath.WalkFunc) error {
	if stat, err := os.Stat(namesDir); err == nil {
		if !stat.IsDir() {
			return fmt.Errorf("%s/%s exists but is not a directory", topdir, namesDir)
		}
		if err := filepath.Walk(namesDir, rec); err != nil {
			return fmt.Errorf("cannot recover data directory %s/%s: %v", topdir, namesDir, err)
		}
		return nil
	}
	if err := os.Mkdir(namesDir, 0700); err != nil {
		return fmt.Errorf("cannot create data directory %s/%s: %v", topdir, namesDir, err)
	}
	return nil
}

func setupSubdir(topdir string, rec filepath.WalkFunc, h byte) error {
	dir := hex.EncodeToString([]byte{h})
	if stat, err := os.Stat(dir); err == nil {
		if !stat.IsDir() {
			return fmt.Errorf("%s/%s exists but is not a directory", topdir, dir)
		}
		if err := filepath.Walk(dir, rec); err != nil {
			return fmt.Errorf("cannot recover data directory %s/%s: %v", topdir, dir, err)
		}
	} else if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("cannot create data directory %s/%s: %v", topdir, dir, err)
	}
	return nil
}
