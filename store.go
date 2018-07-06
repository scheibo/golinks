package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
)

// FileStore provides a simple file-backed implementation of the Store
// interface. The mapping between names and links is written to the file for
// persistence and resiliency to restarts, but cache serves as the in-memory
// representation of the file for serving requests, with the order array
// existing to allow correct iteration. This store also supports the notion of
// 'fuzzy' lookup if initialized with fuzzy - hyphens and underscores and
// capitalization will be ignored in name during lookups. Access to all fields
// except fuzzy must be guarded by lock.
type FileStore struct {
	fuzzy bool
	order []string
	cache map[string]string
	file  *os.File
	lock  *sync.RWMutex
}

// Opens a FileStore backed by filename (and optional fz to enable fuzzy
// lookups). If the file already exists the store will initialize its state
// with the contents, otherwise future calls to Set will write to the file for
// future startups. The FileStore returned should be closed with Close once
// it is no longer in use.
func Open(filename string, fz ...bool) (*FileStore, error) {
	fuzzy := false
	if len(fz) > 0 {
		fuzzy = fz[0]
	}

	s := &FileStore{fuzzy: fuzzy, cache: make(map[string]string)}

	s.lock.Lock()
	defer s.lock.Unlock()

	f, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0755)
	if err != nil {
		return nil, err
	}
	s.file = f

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		split := strings.Split(scanner.Text(), " ")
		s.order = append(s.order, split[0])
		switch len(split) {
		case 1:
			s.set(split[0], "")
		case 2:
			s.set(split[0], split[1])
		default:
			return nil, fmt.Errorf("invalid line in %s: %s", filename, scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close closes the FileStore returned by Open.
func (s *FileStore) Close() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.file.Close()
}

func (s *FileStore) Get(name string) (string, bool) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	link, ok := s.get(name)
	if !ok || link == "" {
		return "", false
	}
	return link, true
}

func (s *FileStore) Set(name, link string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := s.file.WriteString(fmt.Sprintf("%s %s", name, link))
	if err != nil {
		return err
	}
	s.order = append(s.order, name)
	s.set(name, link)
	return nil
}

func (s *FileStore) Iterate(cb func(name, link string) error) error {
	seen := make(map[string]bool)
	for i := len(s.order) - 1; i >= 0; i-- {
		next := s.order[i]
		_, ok := seen[next]
		seen[next] = true
		if !ok {
			link, ok := s.Get(next)
			if ok {
				if err := cb(next, link); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *FileStore) get(name string) (string, bool) {
	link, ok := s.cache[name]
	if (!ok || link == "") && s.fuzzy {
		link, ok = s.cache[fuzz(name)]
	}
	return link, ok
}

func (s *FileStore) set(name, link string) {
	if link == "" {
		delete(s.cache, name)
	} else {
		s.cache[name] = link
	}

	if s.fuzzy {
		fuzzed := fuzz(name)
		if link == "" {
			delete(s.cache, fuzzed)
		} else {
			s.cache[fuzzed] = link
		}
	}
}

func fuzz(name string) string {
	return strings.ToLower(strings.Replace(strings.Replace(name, "-", "", -1), "_", "", -1))
}
