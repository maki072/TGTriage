package service

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"tgtriage/internal/domain"
)

const quarantineTopicName = "🛡 Карантин"

func quarantineTopicMetaKey(groupID int64) string {
	return fmt.Sprintf("hd_quarantine_topic_%d", groupID)
}

// namedTopic returns the id of a service topic of the group remembered under metaKey, creating it
// when missing.
func (s *HelpdeskService) namedTopic(ctx context.Context, mu *sync.Mutex, metaKey, name string) (int64, int, error) {
	group := s.GroupID()
	if group == 0 {
		return 0, 0, domain.ErrHelpdeskOff
	}
	mu.Lock()
	defer mu.Unlock()
	if v, err := s.settings.Meta(ctx, metaKey); err == nil {
		if id, _ := strconv.Atoi(v); id > 0 {
			return group, id, nil
		}
	}
	id, err := s.transport.CreateTopic(ctx, group, name)
	if err != nil {
		return 0, 0, err
	}
	if err := s.settings.SetMeta(ctx, metaKey, strconv.Itoa(id)); err != nil {
		return 0, 0, err
	}
	return group, id, nil
}

// QuarantineTopic returns the group topic where held users' messages wait for a decision.
func (s *HelpdeskService) QuarantineTopic(ctx context.Context) (int64, int, error) {
	return s.namedTopic(ctx, &s.quarantineMu, quarantineTopicMetaKey(s.GroupID()), quarantineTopicName)
}

// forgetQuarantineTopic drops a quarantine topic that turned out to be deleted.
func (s *HelpdeskService) forgetQuarantineTopic(ctx context.Context, groupID int64) {
	if err := s.settings.SetMeta(ctx, quarantineTopicMetaKey(groupID), ""); err != nil {
		s.log.Warn("forget quarantine topic", "err", err)
	}
}
