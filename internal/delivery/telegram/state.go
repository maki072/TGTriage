package tgbot

import (
	"sync"
	"time"
)

type stateKind int

const (
	stateReply stateKind = iota + 1
	stateSnoozeCustom
	stateModel
	stateDebounce
	stateDigestTime
)

const stateTTL = 30 * time.Minute

// dialogState is a pending text input from the owner.
type dialogState struct {
	Kind     stateKind
	TaskID   int64
	Provider string
	expires  time.Time
}

// stateStore keeps the single owner's UI state in memory.
type stateStore struct {
	mu       sync.Mutex
	cur      *dialogState
	lastList listQuery
}

func newStateStore() *stateStore { return &stateStore{lastList: defaultListQuery()} }

func (s *stateStore) set(st dialogState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.expires = time.Now().Add(stateTTL)
	s.cur = &st
}

func (s *stateStore) get() (dialogState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return dialogState{}, false
	}
	if time.Now().After(s.cur.expires) {
		s.cur = nil
		return dialogState{}, false
	}
	return *s.cur, true
}

func (s *stateStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = nil
}

func (s *stateStore) setList(q listQuery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastList = q
}

func (s *stateStore) list() listQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastList
}
