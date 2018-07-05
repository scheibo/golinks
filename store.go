package golinks

import (
	"fmt"
	"os"
	"sync"
)

// Simple file store, use sqlite3 for a more robust backend
type FileStore struct {
	order []string
	cache map[string]string
	file  *os.File
	lock  *sync.RWMutex
}

func Open(filename string) (*FileStore, error) {
	return readFile(&FileStore{})
}

func readFile(s *FileStore) (*FileStore, error) {
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0755)
	if err != nil {
		return nil, err
	}
	s.file = f

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		split := strings.Split(scanner.Text(), " ")
		s.order = append(s.order, name)
		switch len(split) {
		case 1:
			s.set(split[0], "")
			break
		case 2:
			s.set(split[0], split[1])
			break
		default:
			return nil, fmt.Errorf("invalid line in %s: %s", filename, scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
}

func Close() error {
	return file.Close()
}

func (s *FileStore) Get(name string) (string, bool) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	link, ok := s.get(name)
	if !ok || link == "" {
		return nil, false
	}
	return link, true
}

func (s *FileStore) Set(name, link string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := f.WriteString(fmt.Sprintf("%s %s", name, link))
	if err != nil {
		return err
	}
	s.order = append(s.order, name)
	s.set(name, link)
	return nil
}

func (s *FileStore) Iterate(cb func(name, link string) error) error {
	var seen map[int]bool
	for i := len(s.order) - 1; i >= 0; i-- {
		next := order[i]
		_, ok = seen[next]
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
}

func (s *FileStore) get(name) (string, bool) {
	return s.cache[name]
}

func (s *FileStore) set(name, link string) {
	if link == "" {
		delete(s.cache, name)
	} else {
		s.cache[name] = link
	}
}

// TODO dump to new file or stdout, then swap? replace on restarts?
func (s *FileStore) dump() error {
	s.Iterate(func(name, url string) error {
		// TODO wrong, need to reverse!
		return f.WriteString(fmt.Sprintf("%s %s", name, link))
	})
}

// Uses fuzzy matching
type FuzzyFileStore struct {
	*FileStore
}

func FuzzyOpen(filename string) (*FuzzyFileStore, error) {
	return readFile(&FuzzyFileStore{})
}

func (s *FileStore) get(name) (string, bool) {
	link, ok := s.cache[name]
	if !ok || link == "" {
		link, ok = s.cache[fuzz(name)]
	}
	return link, ok
}

func (s *FuzzyFileStore) set(name, link string) {
	fuzzed := fuzz(name)
	if link == "" {
		delete(s.cache, name)
		delete(s.cache, fuzzed)
	} else {
		s.cache[name] = link
		s.cache[fuzzed] = link
	}
}

func fuzz(name string) string {
	return strings.Replace(strings.Replace(name, "-", ""), "_", "")
}
