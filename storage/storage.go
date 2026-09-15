// Copyright (c) 2014-2015, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package storage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// Length of the random hexadecimal ids assigned to pastes. At least 4.
	idSize = 8
	// Maximum length of a paste id, in characters.
	maxIDLength = 200
	// Number of times to try getting an unused random paste id
	randTries = 10
	// Number of times times to retry deleting a paste
	deleteRetries = 5
	// How long to wait before retrying to delete a paste
	deleteRetryTimeout = 1 * time.Minute
)

var (
	// ErrPasteNotFound means that we could not find the requested paste
	ErrPasteNotFound = errors.New("paste could not be found")
	// ErrNoUnusedIDFound means that we could not find an unused ID to
	// allocate to a new paste
	ErrNoUnusedIDFound = errors.New("gave up trying to find an unused random id")
	// ErrInvalidID means that the given paste id is empty or too long
	ErrInvalidID = errors.New("invalid paste id")
)

// A Paste represents the paste's content and information
type Paste interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
	ModTime() time.Time
	Size() int64
}

// ID is the identifier of a paste. It can be any non-empty string of up to
// maxIDLength characters.
type ID string

// NewID validates name and returns it as a paste ID. An error is returned if
// the name is empty or longer than maxIDLength characters.
func NewID(name string) (ID, error) {
	if name == "" || utf8.RuneCountInString(name) > maxIDLength {
		return "", ErrInvalidID
	}
	return ID(name), nil
}

// IDFromString parses a hexadecimal string into an ID. It is used for the
// random ids that pastecat generates on its own. Returns the ID and an error,
// if any.
func IDFromString(hexID string) (id ID, err error) {
	if len(hexID) != idSize {
		return id, fmt.Errorf("invalid id at %s", hexID)
	}
	b, err := hex.DecodeString(hexID)
	if err != nil || len(b) != idSize/2 {
		return id, fmt.Errorf("invalid id at %s", hexID)
	}
	return ID(hexID), nil
}

func (id ID) String() string {
	return string(id)
}

// A Store represents a database holding multiple pastes identified by their
// ids
type Store interface {
	// Get the paste known by the given ID and an error, if any.
	Get(id ID) (Paste, error)

	// Put a new paste given its content. Will return the ID assigned to
	// the new paste and an error, if any.
	Put(content []byte) (ID, error)

	// PutWithID stores content under the given ID, replacing any paste
	// that already exists with that ID. Will return an error, if any.
	PutWithID(id ID, content []byte) error

	// Delete an existing paste by its ID. Will return an error, if any.
	Delete(id ID) error
}

func randomID(available func(ID) bool) (ID, error) {
	for try := 0; try < randTries; try++ {
		b := make([]byte, idSize/2)
		if _, err := rand.Read(b); err != nil {
			continue
		}
		id := ID(hex.EncodeToString(b))
		if available(id) {
			return id, nil
		}
	}
	return "", ErrNoUnusedIDFound
}

// pendingDeletions keeps track of the deletion timers of each paste so that
// they can be stopped and replaced when a paste is overwritten.
var pendingDeletions = struct {
	sync.Mutex
	timers map[ID]*time.Timer
}{timers: make(map[ID]*time.Timer)}

func SetupPasteDeletion(s Store, stats *Stats, id ID, size int64, after time.Duration) {
	pendingDeletions.Lock()
	if t, e := pendingDeletions.timers[id]; e {
		t.Stop()
		delete(pendingDeletions.timers, id)
	}
	pendingDeletions.Unlock()
	// A non-positive lifetime means the paste never expires.
	if after <= 0 {
		return
	}
	f := func() {
		pendingDeletions.Lock()
		delete(pendingDeletions.timers, id)
		pendingDeletions.Unlock()
		del := func() error {
			if err := s.Delete(id); err != nil {
				return err
			}
			stats.FreeSpace(size)
			return nil
		}
		if err := del(); err == nil {
			return
		}
		timer := time.NewTimer(deleteRetryTimeout)
		for i := 0; i < deleteRetries; i++ {
			log.Printf("Could not delete %s, trying again in %s", id, deleteRetryTimeout)
			<-timer.C
			if err := del(); err == nil {
				break
			}
			timer.Reset(deleteRetryTimeout)
		}
		log.Printf("Giving up on deleting %s", id)
	}
	t := time.AfterFunc(after, f)
	pendingDeletions.Lock()
	pendingDeletions.timers[id] = t
	pendingDeletions.Unlock()
}
